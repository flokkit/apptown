package verification

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

// memory-declared reads the contract as AppTown parses it: each part the
// apptown runs from a build or an image declares its own memory. A static
// part declares nothing, an init step takes no resources, and a profile
// alone is not the part's own declaration: wherever no profile applies,
// previews included, that part runs with no limit.
func TestMemoryDeclaredJudgesThePartsOwnDeclaration(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		yaml   string
		want   check.Outcome
		detail string
	}{
		"every built part declares": {
			yaml: "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n    resources: { memory: 256M }\n  worker:\n    build: .\n    command: [\"work\"]\n    resources: { memory: 128M }\n",
			want: check.Conforms, detail: "web 256M",
		},
		"a part declares none": {
			yaml: "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n    resources: { memory: 256M }\n  worker:\n    build: .\n    command: [\"work\"]\n",
			want: check.DoesNotConform, detail: "part worker declares no memory",
		},
		"only a profile declares it": {
			yaml: "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nprofiles:\n  production:\n    web: { resources: { memory: 1G } }\n",
			want: check.DoesNotConform, detail: "part web declares no memory",
		},
		"an imaged part counts": {
			yaml: "app:\n  web:\n    image: nginx:1.27\n    ports: [\"80\"]\n    public: true\n",
			want: check.DoesNotConform, detail: "part web",
		},
		"only static parts": {
			yaml: "app:\n  site:\n    static: .\n    public: true\n",
			want: check.Conforms, detail: "only static parts",
		},
		"an init step needs nothing": {
			yaml: "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n    resources: { memory: 384M }\ninit:\n  - name: migrate\n    build: .\n    command: [\"migrate\"]\n",
			want: check.Conforms, detail: "web 384M",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, err := appcontract.Parse([]byte(tc.yaml))
			if err != nil {
				t.Fatal(err)
			}
			v := judgeMemoryDeclared(c)
			if v.Outcome != tc.want || !strings.Contains(v.Detail, tc.detail) {
				t.Fatalf("got %v %q, want %v containing %q", v.Outcome, v.Detail, tc.want, tc.detail)
			}
		})
	}
}

// Through the runtime evaluator, the contract judged is the tree the
// target names; a tree with no app.yaml, or one that does not parse, is
// unknown with the reason, never a pass.
func TestMemoryDeclaredReadsTheTreeItIsGiven(t *testing.T) {
	t.Parallel()
	o := Runtime{}
	tree := func(app string) string {
		dir := t.TempDir()
		if app != "" {
			if err := os.MkdirAll(filepath.Join(dir, ".apptown"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".apptown", "app.yaml"), []byte(app), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	declared := tree("app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n    resources: { memory: 64M }\n")
	if v := o.Probe(t.Context(), guidanceTarget{ContractDir: declared}, "memory-declared"); v.Outcome != check.Conforms {
		t.Fatalf("a declared tree: %+v", v)
	}
	if v := o.Probe(t.Context(), guidanceTarget{ContractDir: tree("")}, "memory-declared"); v.Outcome != check.CannotAssess || !strings.Contains(v.Detail, "no .apptown/app.yaml") {
		t.Fatalf("a tree with no contract: %+v", v)
	}
	if v := o.Probe(t.Context(), guidanceTarget{ContractDir: tree("app: [")}, "memory-declared"); v.Outcome != check.CannotAssess || !strings.Contains(v.Detail, "could not be read") {
		t.Fatalf("a contract that does not parse: %+v", v)
	}
}

// memory-within judges each part's observed use against the limit its own
// container is held to: under the share conforms with each part's figures;
// at the share, or with no limit, does not conform; use the owner could not
// observe is unknown with its reason, never within. Static parts of the
// running release are the file server's and are not judged; a built file
// server is; a part the release does not name is unknown.
func TestMemoryWithinJudgesEachContainersLimitAgainstItsUse(t *testing.T) {
	t.Parallel()
	const mb = 1 << 20
	seen := func(name string, used, limit int64) deployment.PartMemory {
		return deployment.PartMemory{Part: name, UsedBytes: used, LimitBytes: limit, Known: true}
	}
	built := func(names ...string) map[string]bool {
		m := map[string]bool{}
		for _, n := range names {
			m[n] = false
		}
		return m
	}
	for name, tc := range map[string]struct {
		static map[string]bool
		used   []deployment.PartMemory
		killed map[string]int
		share  int
		want   check.Outcome
		detail string
	}{
		"under the share":                {built("web"), []deployment.PartMemory{seen("web", 210*mb, 512*mb)}, nil, 90, check.Conforms, "web 210Mi of 512Mi"},
		"at the share":                   {built("web"), []deployment.PartMemory{seen("web", 90*mb, 100*mb)}, nil, 90, check.DoesNotConform, "web uses 90Mi of its 100Mi limit (90%)"},
		"a share the check names":        {built("web"), []deployment.PartMemory{seen("web", 85*mb, 100*mb)}, nil, 80, check.DoesNotConform, "at or over 80%"},
		"no limit":                       {built("web"), []deployment.PartMemory{seen("web", 700*mb, 0)}, nil, 90, check.DoesNotConform, "part web runs with no memory limit"},
		"use not observed":               {built("web"), []deployment.PartMemory{{Part: "web", Reason: "its container was replaced while it was being observed"}}, nil, 90, check.CannotAssess, "replaced while it was being observed"},
		"one part unknown, another over": {built("web", "worker"), []deployment.PartMemory{seen("web", 99*mb, 100*mb), {Part: "worker", Reason: "the daemon did not report its container"}}, nil, 90, check.DoesNotConform, "web uses 99M"},
		"only static parts":              {map[string]bool{"site": true}, []deployment.PartMemory{seen("site", 12*mb, 0)}, nil, 90, check.Conforms, "only static parts"},
		"a static part beside a built one is not judged": {map[string]bool{"site": true, "api": false}, []deployment.PartMemory{seen("site", 12*mb, 0), seen("api", 300*mb, 256*mb)}, nil, 90, check.DoesNotConform, "api uses 300Mi of its 256Mi limit"},
		"a built file server is judged":                  {built("game"), []deployment.PartMemory{seen("game", 11*mb, 0)}, nil, 90, check.DoesNotConform, "part game runs with no memory limit"},
		"a part the release does not name":               {map[string]bool{}, []deployment.PartMemory{seen("web", 10*mb, 256*mb)}, nil, 90, check.CannotAssess, "the release's contract does not name it"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			v := judgeMemoryWithin(tc.static, nil, tc.used, tc.killed, tc.share)
			if v.Outcome != tc.want || !strings.Contains(v.Detail, tc.detail) {
				t.Fatalf("got %v %q, want %v containing %q", v.Outcome, v.Detail, tc.want, tc.detail)
			}
		})
	}
}

// memory-within is answered only by a standing deployment: a target that is
// not one (a preview) is unknown, and never reaches the deployment owner.
func TestMemoryWithinNeedsAStandingDeployment(t *testing.T) {
	t.Parallel()
	v := Runtime{}.Probe(t.Context(), guidanceTarget{ContractDir: t.TempDir()}, "memory-within")
	if v.Outcome != check.CannotAssess || !strings.Contains(v.Detail, "standing deployment") {
		t.Fatalf("a target that is not a standing deployment: %+v", v)
	}
}

// A review judges the contract as the project stands: its default branch's
// head, whether or not anything is deployed, never the work tree's edits.
func TestMemoryDeclaredInAReviewReadsTheDefaultBranch(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	repo := filepath.Join(dataDir, "workspaces", "project-"+idstest.N(1).String())
	gittest.Repository(t, repo, "main", gittest.Commit{Message: "declared", Files: map[string]string{
		".apptown/app.yaml": "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n    resources: { memory: 64M }\n",
	}})
	// An uncommitted edit in the work tree is not the project's contract.
	if err := os.WriteFile(filepath.Join(repo, ".apptown", "app.yaml"), []byte("app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := Runtime{dataDir: dataDir}
	if v := o.Probe(t.Context(), guidanceTarget{ProjectID: idstest.N(1), Slug: "demo", Revision: "main"}, "memory-declared"); v.Outcome != check.Conforms || !strings.Contains(v.Detail, "web 64M") {
		t.Fatalf("the default branch's contract: %+v", v)
	}
	if v := o.Probe(t.Context(), guidanceTarget{ProjectID: idstest.N(1), Slug: "demo"}, "memory-declared"); v.Outcome != check.CannotAssess || !strings.Contains(v.Detail, "which commit to judge is not known") {
		t.Fatalf("no branch to read is unknown: %+v", v)
	}
}

// A contract check that fails at the gate is compared with the change's base
// commit itself, with no deployment needed: failing there too it is
// preexisting, passing there the change introduced it. Never main's head,
// and never main's deployment.
func TestAFailingContractCheckIsComparedWithTheBaseCommit(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	repo := filepath.Join(dataDir, "workspaces", "project-"+idstest.N(1).String())
	shas := gittest.Repository(t, repo, "main",
		gittest.Commit{Message: "declared", Files: map[string]string{".apptown/app.yaml": "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n    resources: { memory: 64M }\n"}},
		gittest.Commit{Message: "undeclared", Files: map[string]string{".apptown/app.yaml": "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n"}},
	)
	a := &Guidance{runtime: &Runtime{dataDir: dataDir}}
	sc := conformance.DeclaredCheck{Block: "deploy/memory-declared", Version: 1, Check: conformance.CheckDeclaration{AppliesTo: []string{"repository"}, Name: "memory-declared", Evaluator: "runtime", Probe: "memory-declared", Severity: "medium"}}
	finding := conformance.Finding{Finding: check.Finding{Fingerprint: conformance.Fingerprint(sc, "")}}
	for _, tc := range []struct {
		base string
		want check.Outcome
	}{{shas[0], check.Conforms}, {shas[1], check.DoesNotConform}} {
		got := a.runtimeBaseline(t.Context(), idstest.N(1), "demo", []conformance.DeclaredCheck{sc}, []conformance.Finding{finding}, tc.base)
		if len(got) != 1 || got[0].Outcome != tc.want {
			t.Errorf("base %s: %+v", tc.base[:7], got)
		}
	}
}

// A what-if judges the contract check on the tree it assessed, with no
// running app: a project awaiting its first deploy, or with main ahead of
// its deployment, is still answered, while a probe of the running app in
// the same assessment says why it was not judged.
func TestAWhatIfJudgesTheContractOnTheAssessedTree(t *testing.T) {
	t.Parallel()
	tree := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tree, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, ".apptown", "app.yaml"), []byte("app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	declared := conformance.DeclaredCheck{Block: "deploy/memory-declared", Version: 1, Check: conformance.CheckDeclaration{AppliesTo: []string{"repository"}, Name: "memory-declared", Evaluator: "runtime", Probe: "memory-declared", Severity: "medium"}}
	page := conformance.DeclaredCheck{Block: "run", Version: 1, Check: conformance.CheckDeclaration{AppliesTo: []string{"repository"}, Name: "page", Evaluator: "runtime", Probe: "page", Severity: "high"}}
	f := Fleet{runtime: &Runtime{}}
	got := map[string]treeAssessment{}
	for _, a := range f.assessChecks(t.Context(), []conformance.DeclaredCheck{declared, page}, assessInput{tool: toolContext{BuildDir: tree}, noApp: "no standing main deployment"}) {
		got[a.Check.Check.Name] = a
	}
	if a := got["memory-declared"]; a.State != conformance.DoesNotConform || len(a.Findings) != 1 {
		t.Errorf("the contract check on the assessed tree: %+v", a)
	}
	if a := got["page"]; a.State != conformance.CannotAssess || a.Reason != conformance.NotAssessedNoRunningApp {
		t.Errorf("the running-app probe with no app: %+v", a)
	}
}

// A need is judged as a part is once its generation declared it a limit:
// an undeclared need is not judged, a declared one whose use cannot be read
// is unknown (never within), and one its container does not hold is a
// finding. Every kill in the window counts, whatever its part is called
// now: one recorded before the front door had a name, or of a part since
// removed.
func TestMemoryWithinJudgesNeedsAndEveryKill(t *testing.T) {
	t.Parallel()
	const mb = 1 << 20
	web := deployment.PartMemory{Part: "web", UsedBytes: 100 * mb, LimitBytes: 512 * mb, Known: true}
	pg := func(used, limit int64, known bool) deployment.PartMemory {
		return deployment.PartMemory{Part: "postgres", Need: true, UsedBytes: used, LimitBytes: limit, Known: known, Reason: map[bool]string{false: "the daemon did not report its container"}[known]}
	}
	declared := map[string]bool{"postgres": true}
	for name, tc := range map[string]struct {
		needs  map[string]bool
		used   []deployment.PartMemory
		killed map[string]int
		want   check.Outcome
		detail string
	}{
		"an undeclared need is not judged":                 {needs: nil, used: []deployment.PartMemory{web, pg(30*mb, 0, true)}, want: check.Conforms, detail: "web 100Mi of 512Mi"},
		"an undeclared need is not judged though unread":   {needs: nil, used: []deployment.PartMemory{web, pg(0, 0, false)}, want: check.Conforms, detail: "web 100Mi of 512Mi"},
		"a declared need is judged":                        {needs: declared, used: []deployment.PartMemory{web, pg(250*mb, 256*mb, true)}, want: check.DoesNotConform, detail: "postgres uses 250Mi of its 256Mi limit"},
		"a declared need whose use cannot be read":         {needs: declared, used: []deployment.PartMemory{web, pg(0, 0, false)}, want: check.CannotAssess, detail: "postgres: the daemon did not report its container"},
		"a declared need its container does not hold":      {needs: declared, used: []deployment.PartMemory{web, pg(30*mb, 0, true)}, want: check.DoesNotConform, detail: "postgres runs with no memory limit"},
		"a part killed at its limit, though under it now":  {used: []deployment.PartMemory{web}, killed: map[string]int{"web": 3}, want: check.DoesNotConform, detail: "web was killed out of memory 3 time(s) in the last day"},
		"a kill recorded before the front door had a name": {used: []deployment.PartMemory{web}, killed: map[string]int{"": 1}, want: check.DoesNotConform, detail: "the front door was killed out of memory 1 time(s)"},
		"a kill of a part since removed":                   {used: []deployment.PartMemory{web}, killed: map[string]int{"worker": 2}, want: check.DoesNotConform, detail: "worker was killed out of memory 2 time(s)"},
		"a need killed out of memory":                      {needs: declared, used: []deployment.PartMemory{web, pg(30*mb, 256*mb, true)}, killed: map[string]int{"postgres": 1}, want: check.DoesNotConform, detail: "postgres was killed out of memory 1 time(s)"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			v := judgeMemoryWithin(map[string]bool{"web": false}, tc.needs, tc.used, tc.killed, 90)
			if v.Outcome != tc.want || !strings.Contains(v.Detail, tc.detail) {
				t.Fatalf("got %v %q, want %v containing %q", v.Outcome, v.Detail, tc.want, tc.detail)
			}
		})
	}
}

// Only out-of-memory kills count against a part: an exit is what Docker
// saw, not proof a limit was reached.
func TestOnlyOutOfMemoryKillsAreCounted(t *testing.T) {
	t.Parallel()
	got := killedOutOfMemory([]deployment.LimitCount{{Part: "web", Kind: deployment.LimitOutOfMemory, Count: 2}, {Part: "web", Kind: deployment.LimitExited, Count: 5}, {Part: "", Kind: deployment.LimitOutOfMemory, Count: 1}})
	if got["web"] != 2 || got[""] != 1 || len(got) != 2 {
		t.Fatalf("killed: %v", got)
	}
}
