package changeadds

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec"
)

const contract = `app:
  web:
    build: .
    ports: ["8080"]
    public: true
needs:
  postgres:
    version: "16"
`

const spec = `[project]
name = "shop"
revision = "1.0"

[profiles.default]
STRIPE_KEY = { description = "payments" }
`

// tree is a commit's files for Compare; a path mapped to nil fails to read.
type tree map[string][]byte

func (t tree) File(path string) ([]byte, bool, error) {
	body, ok := t[path]
	if ok && body == nil {
		return nil, false, errors.New("disk on fire")
	}
	return body, ok, nil
}

func (t tree) Contract() ([]byte, bool, error) { return t.File(contractPath) }

func rowNamed(t *testing.T, rows []workflow.AddsRow, name string) workflow.AddsRow {
	t.Helper()
	for _, r := range rows {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no %s row in %+v", name, rows)
	return workflow.AddsRow{}
}

// A change compares its declarations with the commit it starts from: what
// it declares and the base does not is added, the reverse removed, the
// same on both sides neither.
func TestEachRowListsWhatTheHeadDeclaresThatTheBaseDoesNot(t *testing.T) {
	base := tree{contractPath: []byte(`app:
  web:
    build: .
    ports: ["8080"]
    public: true
needs:
  redis: {}
`), "secretspec.toml": []byte(spec)}
	head := tree{contractPath: []byte(`app:
  web:
    build: .
    ports: ["9090"]
    public: true
    dataDir: /data
  worker:
    build: .
init:
  - name: migrate
    build: .
needs:
  postgres:
    version: "16"
`), "secretspec.toml": []byte(spec + "SENDGRID_KEY = { description = \"mail\" }\n")}
	rows := Compare(base, head, "base", "head")
	if got := rowNamed(t, rows, Secrets); !slices.Equal(got.Added, []string{"SENDGRID_KEY"}) || len(got.Removed) != 0 || got.Unknown != "" {
		t.Errorf("secrets: %+v", got)
	}
	if got := rowNamed(t, rows, Services); !slices.Equal(got.Added, []string{"postgres"}) || !slices.Equal(got.Removed, []string{"redis"}) {
		t.Errorf("services: %+v", got)
	}
	parts := rowNamed(t, rows, Parts)
	wantAdded := []string{"init step migrate", "part worker", "web keeps data in /data", "web listens on port 9090"}
	if !slices.Equal(parts.Added, wantAdded) || !slices.Equal(parts.Removed, []string{"web listens on port 8080"}) {
		t.Errorf("app parts: %+v", parts)
	}
	same := Compare(head, head, "a", "b")
	for _, r := range same {
		if len(r.Added)+len(r.Removed) != 0 || r.Unknown != "" {
			t.Errorf("identical commits differ in %s: %+v", r.Name, r)
		}
	}
}

// A file absent on one side declares nothing there: it is a fact, not an
// unknown, and everything the other side declares is added or removed.
func TestAnAbsentDeclarationDeclaresNothing(t *testing.T) {
	head := tree{contractPath: []byte(contract), "secretspec.toml": []byte(spec)}
	rows := Compare(tree{}, head, "base", "head")
	for _, r := range rows {
		if r.Unknown != "" || len(r.Added) == 0 {
			t.Errorf("%s from nothing: %+v", r.Name, r)
		}
	}
	rows = Compare(head, tree{}, "base", "head")
	for _, r := range rows {
		if r.Unknown != "" || len(r.Removed) == 0 || len(r.Added) != 0 {
			t.Errorf("%s to nothing: %+v", r.Name, r)
		}
	}
}

// A declaration that cannot be read or parsed on either side makes its row
// unknown, with the commit and the reason; it never lists nothing as if
// nothing were added. The secrets file is named in the contract, so a
// contract that cannot be read leaves the secrets unknown too.
func TestADeclarationThatCannotBeReadIsUnknownNotEmpty(t *testing.T) {
	good := tree{contractPath: []byte(contract), "secretspec.toml": []byte(spec)}
	cases := map[string]struct {
		head    tree
		unknown []string
	}{
		"contract does not parse": {tree{contractPath: []byte("app: [nope"), "secretspec.toml": []byte(spec)}, []string{Secrets, Services, Parts}},
		"contract unreadable":     {tree{contractPath: nil, "secretspec.toml": []byte(spec)}, []string{Secrets, Services, Parts}},
		"secrets do not parse":    {tree{contractPath: []byte(contract), "secretspec.toml": []byte("[project]\n")}, []string{Secrets}},
		"secrets unreadable":      {tree{contractPath: []byte(contract), "secretspec.toml": nil}, []string{Secrets}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			for _, rows := range [][]workflow.AddsRow{Compare(good, c.head, "b1", "h1"), Compare(c.head, c.head, "h1", "h1")} {
				for _, r := range rows {
					want := slices.Contains(c.unknown, r.Name)
					if (r.Unknown != "") != want {
						t.Errorf("%s unknown=%q, want unknown %v", r.Name, r.Unknown, want)
					}
					if want && !strings.Contains(r.Unknown, "h1") {
						t.Errorf("%s does not name the commit: %q", r.Name, r.Unknown)
					}
					if want && len(r.Added)+len(r.Removed) != 0 {
						t.Errorf("%s lists what it could not read: %+v", r.Name, r)
					}
				}
			}
		})
	}
}

// When only main's side cannot be read (a change repairing a file AppTown
// now refuses), the change's own declarations are still what the approver
// accepts: each row lists everything the change declares and names why
// main's side is not known, instead of turning unknown.
func TestAnUnreadableMainStillShowsWhatTheChangeDeclares(t *testing.T) {
	good := tree{contractPath: []byte(contract), "secretspec.toml": []byte(spec)}
	refused := tree{contractPath: []byte("app: [nope"), "secretspec.toml": []byte(spec)}
	rows := Compare(refused, good, "b1", "h1")
	for name, want := range map[string][]string{
		Secrets:  {"STRIPE_KEY"},
		Services: {"postgres"},
		Parts:    {"part web", "web is the public part"},
	} {
		r := rowNamed(t, rows, name)
		if r.Unknown != "" || r.BaseUnread == "" || !strings.Contains(r.BaseUnread, "b1") {
			t.Errorf("%s: %+v; want read, with main's reason", name, r)
		}
		for _, w := range want {
			if !slices.Contains(r.Added, w) {
				t.Errorf("%s does not list %q: %+v", name, w, r.Added)
			}
		}
		if len(r.Removed) != 0 {
			t.Errorf("%s claims removals against an unreadable main: %+v", name, r.Removed)
		}
	}
}

// The secrets file is where the contract says it is.
func TestTheSecretsFileIsTheOneTheContractNames(t *testing.T) {
	head := tree{contractPath: []byte(contract + "secrets:\n  spec: config/secrets.toml\n"), "config/secrets.toml": []byte(spec)}
	if got := rowNamed(t, Compare(tree{}, head, "b", "h"), Secrets); !slices.Equal(got.Added, []string{"STRIPE_KEY"}) {
		t.Fatalf("secrets: %+v", got)
	}
}

// An uplift's row is read from its recorded adoption target: what it brings
// and what it retires, including a workflow retired without any guidance
// block going.
func TestAnUpliftShowsWhatItsRecordedTargetBringsAndRetires(t *testing.T) {
	raw, err := json.Marshal(adoption.Candidate{Added: []string{"go-small@2"}, Changed: []string{"forge v2→v3"}, LanesAdded: []string{"hotfix"}, LanesRemoved: []string{"legacy-deploy"},
		NewChecks: []adoption.CheckSummary{{Block: "go-small", Check: "go-vet"}}})
	if err != nil {
		t.Fatal(err)
	}
	row := waysRow(raw)
	wantAdded := []string{"check go-small · go-vet", "forge v2→v3", "go-small@2", "workflow hotfix"}
	if row.Unknown != "" || !slices.Equal(row.Added, wantAdded) || !slices.Equal(row.Removed, []string{"workflow legacy-deploy"}) {
		t.Fatalf("ways of working: %+v", row)
	}
	if row := waysRow(json.RawMessage(`{"added": 3}`)); row.Unknown == "" || len(row.Added) != 0 {
		t.Fatalf("an unreadable target is not unknown: %+v", row)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if dir == "" {
		t.Fatal("git needs a directory")
	}
	out, err := gitexec.Run(t.Context(), dir, append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(out)
}

func commitFiles(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	for path, body := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "files")
	return git(t, dir, "rev-parse", "HEAD")
}

// Against a real repository: the base is where the change meets main, so
// a service main gained on its own is neither added nor removed by the
// change; a commit neither repository holds makes every row unknown; a
// declaration that is a link is not followed.
func TestTheComparerReadsFromWhereTheChangeMeetsMain(t *testing.T) {
	ctx := context.Background()
	hub := t.TempDir()
	git(t, hub, "init", "-q", "-b", "main")
	commitFiles(t, hub, map[string]string{contractPath: contract})
	git(t, hub, "checkout", "-q", "-b", "change")
	head := commitFiles(t, hub, map[string]string{"secretspec.toml": spec})
	git(t, hub, "checkout", "-q", "main")
	commitFiles(t, hub, map[string]string{contractPath: strings.Replace(contract, "postgres", "redis", 1)})

	q := workflow.AddsQuestion{Head: head, DefaultBranch: "main", Hub: hub, Repositories: []string{"", hub}}
	adds, err := Comparer{}.ChangeAdds(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if adds.Head != head || adds.Base == "" {
		t.Fatalf("commits: %+v", adds)
	}
	if got := rowNamed(t, adds.Rows, Services); len(got.Added)+len(got.Removed) != 0 || got.Unknown != "" {
		t.Errorf("main's own change is blamed on the change: %+v", got)
	}
	if got := rowNamed(t, adds.Rows, Secrets); !slices.Equal(got.Added, []string{"STRIPE_KEY"}) {
		t.Errorf("secrets: %+v", got)
	}
	if len(adds.Rows) != 3 {
		t.Errorf("a change that is not an uplift has a ways-of-working row: %+v", adds.Rows)
	}

	q.Head = strings.Repeat("ab", 20)
	adds, err = Comparer{}.ChangeAdds(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range adds.Rows {
		if r.Unknown == "" {
			t.Errorf("%s read for a commit no repository holds: %+v", r.Name, r)
		}
	}

	git(t, hub, "checkout", "-q", "change")
	if err := os.Remove(filepath.Join(hub, "secretspec.toml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(hub, "secretspec.toml")); err != nil {
		t.Fatal(err)
	}
	git(t, hub, "add", "-A")
	git(t, hub, "commit", "-qm", "link")
	q.Head = git(t, hub, "rev-parse", "HEAD")
	adds, err = Comparer{}.ChangeAdds(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if got := rowNamed(t, adds.Rows, Secrets); !strings.Contains(got.Unknown, "link") {
		t.Errorf("a linked secrets file was read: %+v", got)
	}
}

// Main before its first commit is an empty base: everything the change
// declares is added.
func TestAChangeOnAnEmptyMainAddsEverythingItDeclares(t *testing.T) {
	hub := t.TempDir()
	git(t, hub, "init", "-q", "-b", "work")
	head := commitFiles(t, hub, map[string]string{contractPath: contract})
	adds, err := Comparer{}.ChangeAdds(context.Background(), workflow.AddsQuestion{Head: head, DefaultBranch: "main", Hub: hub, Repositories: []string{hub}})
	if err != nil {
		t.Fatal(err)
	}
	if adds.Base != "" || !slices.Equal(rowNamed(t, adds.Rows, Services).Added, []string{"postgres"}) {
		t.Fatalf("empty main: %+v", adds)
	}
}

// A folder on a declaration's path that is a link is not read through, and
// not taken as the file being absent: the row is unknown.
func TestALinkedFolderMakesItsDeclarationUnknownNotEmpty(t *testing.T) {
	ctx := context.Background()
	for name, files := range map[string]struct {
		contract, link, target string
		unknown                []string
	}{
		"secrets folder":  {contract + "secrets:\n  spec: config/secrets.toml\n", "config", "real", []string{Secrets}},
		".apptown folder": {"", ".apptown", "real", []string{Secrets, Services, Parts}},
	} {
		t.Run(name, func(t *testing.T) {
			hub := t.TempDir()
			git(t, hub, "init", "-q", "-b", "main")
			commitFiles(t, hub, map[string]string{"README": "x"})
			git(t, hub, "checkout", "-q", "-b", "change")
			body := map[string]string{"real/secrets.toml": spec, "real/app.yaml": contract}
			if files.contract != "" {
				body[contractPath] = files.contract
			}
			commitFiles(t, hub, body)
			if err := os.Symlink(files.target, filepath.Join(hub, files.link)); err != nil {
				t.Fatal(err)
			}
			git(t, hub, "add", "-A")
			git(t, hub, "commit", "-qm", "link")
			head := git(t, hub, "rev-parse", "HEAD")
			adds, err := Comparer{}.ChangeAdds(ctx, workflow.AddsQuestion{Head: head, DefaultBranch: "main", Hub: hub, Repositories: []string{hub}})
			if err != nil {
				t.Fatal(err)
			}
			saysLink := false
			for _, r := range adds.Rows {
				want := slices.Contains(files.unknown, r.Name)
				saysLink = saysLink || strings.Contains(r.Unknown, "is a link")
				if want && r.Unknown == "" {
					t.Errorf("%s through a linked folder: %+v", r.Name, r)
				}
				if !want && r.Unknown != "" {
					t.Errorf("%s unknown: %+v", r.Name, r)
				}
			}
			if !saysLink {
				t.Errorf("no row says a folder is a link: %+v", adds.Rows)
			}
		})
	}
}
