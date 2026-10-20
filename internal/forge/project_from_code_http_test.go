package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/projectlifecycle"
	"flokkit.io/forge/internal/forge/scm"
)

// codeBundle is an existing repository's history, main and a tag, as
// `git bundle create p.bundle main --tags` makes it.
func codeBundle(t *testing.T) (bundle []byte, main string) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		cmd := gittest.Command(append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "secretspec.toml"), []byte("[project]\nname = \"x\"\nrevision = \"1.0\"\n\n[profiles.default]\nSTRIPE_KEY = { description = \"payments\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "existing code")
	git("tag", "v1")
	main = git("rev-parse", "HEAD")
	git("bundle", "create", "-q", "p.bundle", "main", "--tags")
	b, err := os.ReadFile(filepath.Join(dir, "p.bundle"))
	if err != nil {
		t.Fatal(err)
	}
	return b, main
}

// callAs serves one request through the production routes as a verified
// identity, with no middleware deciding who it is.
func callAs(t *testing.T, mux http.Handler, id *authIdentity, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(context.WithValue(r.Context(), authCtxKey{}, id))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// Filling a new project's repository from existing code is project:import
// for the caller's own credential: a read-only connected agent is refused
// even for the project's owner or an administrator, as is a chat's session
// and a person with no part in the project; each refusal fetches and
// writes nothing. The owner's own session seeds it once, the default
// branch and tags at the source's commits, with a receipt.
func TestStartingFromExistingCodeIsProjectImportForTheCaller(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	mux, err := newRouteMux(a, appRoutes(a))
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "moved", Actor: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	hub := filepath.Join(a.cfg.dataDir, "workspaces", "project-moved")
	bundle, main := codeBundle(t)
	seedPath := "/api/v1/projects/moved/repo/from-bundle"
	for name, id := range map[string]*authIdentity{
		"a person with no part in the project":  {User: "outsider", Via: "session"},
		"the owner's read-only connected agent": {User: "owner", Via: "oauth", Scopes: []string{"apptown:user:read"}},
		"an administrator's read-only agent":    {User: "boss", Via: "oauth", Scopes: []string{"apptown:user:read"}},
		"a chat session in the project":         {User: "owner", Via: "console", ProjectID: p.ID, ConsoleID: "c"},
		"a step in the project":                 {User: "owner", Via: "run", ProjectID: p.ID},
	} {
		if w := callAs(t, mux, id, "POST", seedPath, bundle); w.Code != 403 && w.Code != 401 {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
		if _, err := os.Stat(hub); !os.IsNotExist(err) {
			t.Fatalf("%s: a refused seed wrote the hub: %v", name, err)
		}
	}
	owner := &authIdentity{User: "owner", Via: "session"}
	// Naming a apptown secret as the git credential is for administrators:
	// refused before the URL is even looked at.
	if w := callAs(t, mux, owner, "POST", "/api/v1/projects/moved/repo/from-url", []byte(`{"url":"https://example.invalid/x.git","credentialSecret":"DIGITALOCEAN_TOKEN"}`)); w.Code != 403 {
		t.Fatalf("a non-administrator named a secret: %d %s", w.Code, w.Body.String())
	}
	// The server does not fetch from its own machine.
	if w := callAs(t, mux, owner, "POST", "/api/v1/projects/moved/repo/from-url", []byte(`{"url":"https://127.0.0.1/x.git"}`)); w.Code != 400 || !strings.Contains(w.Body.String(), "public addresses") {
		t.Fatalf("a loopback URL: %d %s", w.Code, w.Body.String())
	}
	// An administrator naming a secret the forge does not hold is told so.
	if w := callAs(t, mux, &authIdentity{User: "boss", Via: "session"}, "POST", "/api/v1/projects/moved/repo/from-url", []byte(`{"url":"https://93.184.215.14/x.git","credentialSecret":"NO_SUCH_SECRET"}`)); w.Code != 400 || !strings.Contains(w.Body.String(), "no secret") {
		t.Fatalf("a missing secret: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(hub); !os.IsNotExist(err) {
		t.Fatalf("a refused URL wrote the hub: %v", err)
	}

	if w := callAs(t, mux, owner, "POST", seedPath, bundle); w.Code != 200 {
		t.Fatalf("the owner's seed: %d %s", w.Code, w.Body.String())
	}
	seeded, err := a.Projects.BySlug(context.Background(), "moved")
	if err != nil || seeded.DefaultBranch != "main" {
		t.Fatalf("seeded project: %+v %v", seeded, err)
	}
	wantMainRecorded(t, a, "moved", scm.ViaSeed)
	if got := gitOut(t, hub, "rev-parse", "v1^{commit}"); got != main {
		t.Fatalf("v1 = %s, want %s", got, main)
	}
	if got := gitOut(t, hub, "merge-base", "--is-ancestor", main, "main"); got != "" {
		t.Fatalf("main does not carry the source's history: %s", got)
	}
	receipts, err := audit.Recent(context.Background(), a.DB, audit.Filter{ProjectID: p.ID, KindPrefix: "project.repo.seeded"})
	if err != nil || len(receipts) != 1 || receipts[0].Actor != "owner" {
		t.Fatalf("seed receipts: %+v %v", receipts, err)
	}
	// Once the project has history, it is never replaced.
	if w := callAs(t, mux, owner, "POST", seedPath, bundle); w.Code != 409 {
		t.Fatalf("a second seed: %d %s", w.Code, w.Body.String())
	}
	// The declared secret names are there to set.
	if w := callAs(t, mux, owner, "GET", "/api/v1/projects/moved/secrets", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "STRIPE_KEY") {
		t.Fatalf("declared secrets: %d %s", w.Code, w.Body.String())
	}
}

// Download code is the project's default branch and tags as a bundle, for
// anyone who may read the project (a read-only agent included) and no one
// else; that bundle starts another project at the same commits.
func TestDownloadCodeStartsAnotherProjectUnchanged(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	mux, err := newRouteMux(a, appRoutes(a))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "source", Actor: "owner"}); err != nil {
		t.Fatal(err)
	}
	bundle, main := codeBundle(t)
	owner := &authIdentity{User: "owner", Via: "session"}
	if w := callAs(t, mux, owner, "GET", "/api/v1/projects/source/repo/bundle", nil); w.Code != 422 || !strings.Contains(w.Body.String(), "not repo-backed") {
		t.Fatalf("a project with no repository: %d %s", w.Code, w.Body.String())
	}
	if w := callAs(t, mux, owner, "POST", "/api/v1/projects/source/repo/from-bundle", bundle); w.Code != 200 {
		t.Fatalf("seed: %d %s", w.Code, w.Body.String())
	}
	// Download follows project:read exactly: whoever reads the project
	// (here everyone signed in, by the instance's grants) downloads it;
	// a chat in another project does not.
	outsider := &authIdentity{User: "outsider", Via: "session"}
	if read, got := callAs(t, mux, outsider, "GET", "/api/v1/projects/source", nil).Code, callAs(t, mux, outsider, "GET", "/api/v1/projects/source/repo/bundle", nil).Code; (read == 200) != (got == 200) {
		t.Fatalf("reading the project = %d but downloading it = %d", read, got)
	}
	if w := callAs(t, mux, &authIdentity{User: "owner", Via: "console", ProjectID: idstest.N(9999), ConsoleID: "c"}, "GET", "/api/v1/projects/source/repo/bundle", nil); w.Code == 200 {
		t.Fatal("a chat in another project downloaded the code")
	}
	w := callAs(t, mux, &authIdentity{User: "owner", Via: "oauth", Scopes: []string{"apptown:user:read"}}, "GET", "/api/v1/projects/source/repo/bundle", nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/x-git-bundle" {
		t.Fatalf("download: %d %s", w.Code, w.Body.String())
	}
	downloaded := w.Body.Bytes()
	if _, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "copy", Actor: "owner"}); err != nil {
		t.Fatal(err)
	}
	if w := callAs(t, mux, owner, "POST", "/api/v1/projects/copy/repo/from-bundle", downloaded); w.Code != 200 {
		t.Fatalf("seed from the download: %d %s", w.Code, w.Body.String())
	}
	src := filepath.Join(a.cfg.dataDir, "workspaces", "project-source")
	dst := filepath.Join(a.cfg.dataDir, "workspaces", "project-copy")
	if gitOut(t, src, "rev-parse", "main") != gitOut(t, dst, "rev-parse", "main") || gitOut(t, dst, "rev-parse", "v1^{commit}") != main {
		t.Fatal("the downloaded bundle changed the history")
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gittest.Command(append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

// linkingBundle is code whose tree links out of itself: a .gitignore and a
// Dockerfile that point at files not yet made outside, a mise.toml that
// points at one, and a .forge that points at a directory outside. Git
// keeps links as links; the forge must never write through them.
func linkingBundle(t *testing.T, outside string) []byte {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := gittest.Command(append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	git("init", "-q", "-b", "main")
	for name, target := range map[string]string{
		".gitignore": filepath.Join(outside, "ignore"),
		"Dockerfile": filepath.Join(outside, "dockerfile"),
		"mise.toml":  filepath.Join(outside, "mise"),
		".forge":     filepath.Join(outside, "forgedir"),
	} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-qm", "links out")
	git("bundle", "create", "-q", "p.bundle", "main")
	b, err := os.ReadFile(filepath.Join(dir, "p.bundle"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Setting up a project started from code, then giving it a toolchain and a
// pack, writes nothing outside its repository through a link its code
// holds. A starter file that would pass through a link is skipped and
// named; the pack's other starter files are still written.
func TestSetupNeverWritesThroughALinkInImportedCode(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	a.SyncStock(t.Context())
	mux, err := newRouteMux(a, appRoutes(a))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "linked", Actor: "owner"}); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "forgedir"), 0o755); err != nil {
		t.Fatal(err)
	}
	owner := &authIdentity{User: "owner", Via: "session"}
	if w := callAs(t, mux, owner, "POST", "/api/v1/projects/linked/repo/from-bundle", linkingBundle(t, outside)); w.Code != 200 {
		t.Fatalf("seed: %d %s", w.Code, w.Body.String())
	}
	callAs(t, mux, owner, "POST", "/api/v1/projects/linked/toolchain", []byte(`{"name":"go"}`))
	w := callAs(t, mux, owner, "POST", "/api/v1/projects/linked/pack", []byte(`{"name":"go-small"}`))
	var applied struct {
		Seeded, Missing []string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &applied); err != nil || w.Code != 200 {
		t.Fatalf("pack: %d %s", w.Code, w.Body.String())
	}
	if !slices.Contains(applied.Seeded, "README.md") ||
		!slices.ContainsFunc(applied.Missing, func(m string) bool { return strings.Contains(m, ".forge/app.yaml not written") }) ||
		!slices.ContainsFunc(applied.Missing, func(m string) bool { return strings.Contains(m, ".gitignore not written") }) {
		t.Fatalf("the pack should write the rest and name each refused file: %+v", applied)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	var made []string
	for _, e := range entries {
		made = append(made, e.Name())
	}
	inside, _ := os.ReadDir(filepath.Join(outside, "forgedir"))
	if len(made) != 1 || made[0] != "forgedir" || len(inside) != 0 {
		t.Fatalf("setup wrote outside the repository: %v, forgedir %v", made, inside)
	}
	hub := filepath.Join(a.cfg.dataDir, "workspaces", "project-linked")
	if got := gitOut(t, hub, "show", "main:README.md"); got == "" {
		t.Fatal("the written starter file was not committed")
	}
	if info, err := os.Lstat(filepath.Join(hub, ".gitignore")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the repository's own .gitignore link was replaced: %v %v", info, err)
	}
}

// A setup that fails after the code arrived leaves what it says: no
// repository. The seed can be repeated, and then the code downloads.
func TestASetupFailureAfterTheFetchLeavesNoRepository(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	mux, err := newRouteMux(a, appRoutes(a))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "flaky", Actor: "owner"}); err != nil {
		t.Fatal(err)
	}
	bundle, _ := codeBundle(t)
	owner := &authIdentity{User: "owner", Via: "session"}
	restore := a.DB.FailNextStatement("UPDATE projects SET default_branch")
	w := callAs(t, mux, owner, "POST", "/api/v1/projects/flaky/repo/from-bundle", bundle)
	restore()
	if w.Code < 400 || !strings.Contains(w.Body.String(), "still has no repository") {
		t.Fatalf("the failed setup: %d %s", w.Code, w.Body.String())
	}
	hub := filepath.Join(a.cfg.dataDir, "workspaces", "project-flaky")
	if _, err := os.Stat(hub); !os.IsNotExist(err) {
		t.Fatalf("the failed setup left the history: %v", err)
	}
	if p, err := a.Projects.BySlug(context.Background(), "flaky"); err != nil || p.DefaultBranch != "" {
		t.Fatalf("project after the failure: %+v %v", p, err)
	}
	if w := callAs(t, mux, owner, "POST", "/api/v1/projects/flaky/repo/from-bundle", bundle); w.Code != 200 {
		t.Fatalf("the repeated seed: %d %s", w.Code, w.Body.String())
	}
	if w := callAs(t, mux, owner, "GET", "/api/v1/projects/flaky/repo/bundle", nil); w.Code != 200 {
		t.Fatalf("download after the repeat: %d %s", w.Code, w.Body.String())
	}
}
