package repositoryedit

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/internal/gitexec/gittest"

	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
)

func TestRemediationDependencyChangesAreOutOfScope(t *testing.T) {
	t.Parallel()
	moved := func(names ...string) []ChangedFile {
		var out []ChangedFile
		for _, n := range names {
			out = append(out, ChangedFile{Path: n})
		}
		return out
	}
	changed := moved("config/deployments/production.rb", "Gemfile.lock", ".gitignore", "web/package-lock.json")
	target := workflow.AcceptanceScopeOf(&workflow.Change{AcceptanceJSON: `{"checks":[{"block":"deploy/health-declared","check":"readiness-redirect-safe"}]}`})
	got := DependencyChangesOutOfScope(changed, target)
	if len(got) != 2 || got[0] != "Gemfile.lock" || got[1] != "web/package-lock.json" {
		t.Fatalf("want the two lockfiles, got %v", got)
	}
	added := []ChangedFile{{Path: "requirements.txt", Additive: true}, {Path: "config/settings.py"}}
	if got := DependencyChangesOutOfScope(added, target); len(got) != 0 {
		t.Fatalf("adding a package the fix needs is in scope, got %v", got)
	}
	deps := workflow.AcceptanceScopeOf(&workflow.Change{AcceptanceJSON: `{"checks":[{"block":"deps/minimal-deps","check":"dependency-budget"}]}`})
	if got := DependencyChangesOutOfScope(changed, deps); len(got) != 0 {
		t.Fatalf("a dependency check may move dependencies, got %v", got)
	}
	tool := workflow.AcceptanceScopeOf(&workflow.Change{AcceptanceJSON: `{"checks":[{"block":"build/toolchain-mise","check":"toolchain-runtime-drift"}]}`})
	if got := DependencyChangesOutOfScope(moved("mise.toml"), tool); len(got) != 0 {
		t.Fatalf("a toolchain check may edit mise.toml, got %v", got)
	}
	if got := DependencyChangesOutOfScope(changed, workflow.AcceptanceScope{}); len(got) != 0 {
		t.Fatalf("an ordinary change is not scoped this way, got %v", got)
	}
	// What the repository ignores is refused on every change, ordinary or
	// not; the forge keeps no list of build folder names.
	built := []ChangedFile{{Path: "README.md"}, {Path: "target/surefire-reports/TEST-x.xml", Ignored: true}, {Path: "web/dist/app.js", Ignored: true}}
	if got := IgnoredChanges(built); len(got) != 2 || got[0] != "target/surefire-reports/TEST-x.xml" {
		t.Fatalf("ignored files are never a change's to commit, got %v", got)
	}
	if got := DependencyChangesOutOfScope(built, deps); len(got) != 0 {
		t.Fatalf("the dependency rule is about dependency files only, got %v", got)
	}
}

// ChangedFilesSince asks git which of the changed files the repository's
// own rules ignore: a force-added target/ report under a `target/` rule is
// reported, a source file beside it is not, and a directory rule covers
// the files beneath it.
func TestChangedFilesSinceMarksWhatTheRepositoryIgnores(t *testing.T) {
	t.Parallel()
	wt := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := gittest.Command(args...)
		cmd.Dir = wt
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(wt, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	write(".gitignore", "target/\n.apptown/*\n!.apptown/app.yaml\n")
	write("README.md", "hello")
	write(".apptown/compose.yaml", "services: {}")
	run("add", "-A")
	run("add", "-f", "--", ".apptown/compose.yaml")
	run("commit", "-q", "-m", "base")
	base, _ := gitexec.Run(t.Context(), wt, "rev-parse", "HEAD")
	write("README.md", "hello again")
	write("src/Main.java", "class Main {}")
	write("target/surefire-reports/TEST-x.xml", "<xml/>")
	write(".apptown/CHANGE.md", "report")
	write(".apptown/app.yaml", "app: {}")
	run("add", "-A")
	run("add", "-f", "--", "target/surefire-reports/TEST-x.xml", ".apptown/CHANGE.md", ".apptown/app.yaml")
	run("rm", "-q", "--cached", ".apptown/compose.yaml") // the deletion is the fix, never the fault
	run("commit", "-q", "-m", "change")
	changed, err := ChangedFilesSince(t.Context(), wt, strings.TrimSpace(base))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range changed {
		got[c.Path] = c.Ignored
	}
	want := map[string]bool{"README.md": false, "src/Main.java": false, "target/surefire-reports/TEST-x.xml": true, ".apptown/CHANGE.md": true, ".apptown/app.yaml": false, ".apptown/compose.yaml": false}
	for p, ig := range want {
		if v, ok := got[p]; !ok || v != ig {
			t.Fatalf("%s: want ignored=%v, got %v (present %v); all %v", p, ig, v, ok, got)
		}
	}
}

// The scope guard reads an empty list as "nothing to refuse", so a list git
// could not produce is an error, never empty: a base commit the repository
// does not have, a workspace that is not a repository, and an ignore question
// git cannot answer each fail.
func TestChangedFilesSinceReportsWhatItCouldNotRead(t *testing.T) {
	t.Parallel()
	wt := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := gittest.Command(args...)
		cmd.Dir = wt
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "base")
	missing := strings.Repeat("0123456789", 4)
	if changed, err := ChangedFilesSince(t.Context(), wt, missing); err == nil {
		t.Fatalf("a base the repository does not have must be an error, got %v", changed)
	}
	if changed, err := ChangedFilesSince(t.Context(), t.TempDir(), missing); err == nil {
		t.Fatalf("a workspace that is not a repository must be an error, got %v", changed)
	}
	// The diff can be read but the ignore question cannot: an exclude file
	// that is a directory makes check-ignore fail outright (exit 128).
	base, err := gitexec.Run(t.Context(), wt, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "src.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "change")
	// Nothing ignored is an answer (check-ignore exits 1), not a failure.
	if changed, err := ChangedFilesSince(t.Context(), wt, strings.TrimSpace(base)); err != nil || len(changed) != 1 || changed[0].Ignored {
		t.Fatalf("a change that commits nothing ignored reads cleanly, got %v %v", changed, err)
	}
	exclude := filepath.Join(wt, ".git", "info", "exclude")
	if err := os.RemoveAll(exclude); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(exclude, 0o755); err != nil {
		t.Fatal(err)
	}
	if changed, err := ChangedFilesSince(t.Context(), wt, strings.TrimSpace(base)); err == nil {
		t.Fatalf("an ignore question git could not answer must be an error, got %v", changed)
	}
}

// A result with no folder is read in the hub: what rev changed against the
// change's base, with the ignore question answered by the .gitignore files
// at rev (nested ones too), never by the hub's working tree or its exclude
// file. What cannot be read is an error, never an empty list: no recorded
// base, a base or revision the hub lacks, and ignore rules whose blob is
// gone.
func TestChangedFilesInHubReadsTheRevisionsOwnRules(t *testing.T) {
	t.Parallel()
	hub := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := gittest.Command(args...)
		cmd.Dir = hub
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(hub, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	write(".gitignore", "target/\n")
	write("web/.gitignore", "dist/\n")
	write("README.md", "hello")
	run("add", "-A")
	run("commit", "-q", "-m", "base")
	base := run("rev-parse", "HEAD")
	write("README.md", "hello again")
	write("target/out.class", "x")
	write("web/dist/app.js", "x")
	write("web/src/app.ts", "x")
	run("add", "-A")
	run("add", "-f", "--", "target/out.class", "web/dist/app.js")
	run("commit", "-q", "-m", "change")
	rev := run("rev-parse", "HEAD")
	// The hub's own tree and exclude file say otherwise; neither is read.
	write(".gitignore", "README.md\n")
	write(".git/info/exclude", "web/src/\n")

	changed, err := ChangedFilesInHub(context.Background(), hub, base, rev)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range changed {
		got[c.Path] = c.Ignored
	}
	want := map[string]bool{"README.md": false, "target/out.class": true, "web/dist/app.js": true, "web/src/app.ts": false}
	if len(got) != len(want) {
		t.Fatalf("changed: %v", got)
	}
	for p, ignored := range want {
		if v, ok := got[p]; !ok || v != ignored {
			t.Errorf("%s: want ignored=%v, got %v (present %v)", p, ignored, v, ok)
		}
	}

	missing := strings.Repeat("0123456789", 4)
	for name, c := range map[string]struct{ base, rev string }{
		"no recorded base": {"", rev}, "a base the hub lacks": {missing, rev}, "a revision the hub lacks": {base, missing},
	} {
		if changed, err := ChangedFilesInHub(context.Background(), hub, c.base, c.rev); err == nil {
			t.Errorf("%s: an empty scope %v, not an error", name, changed)
		}
	}
	blob := run("rev-parse", rev+":web/.gitignore")
	if err := os.Remove(filepath.Join(hub, ".git", "objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}
	if changed, err := ChangedFilesInHub(context.Background(), hub, base, rev); err == nil || !strings.Contains(err.Error(), "web/.gitignore") {
		t.Fatalf("unreadable ignore rules: %v %v", changed, err)
	}
}

// Every reader judges and names the exact path committed: spaces, tabs,
// quotes, a newline and non-ASCII survive git's diff and its ignore matcher
// unchanged, in the folder, at a pinned revision and in the hub. A
// force-added "target/out file.class" under a target/ rule was once read
// as "file.class" and passed.
func TestChangedFilesKeepEveryPathExactly(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := gittest.Command(args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	write(".gitignore", "target/\n")
	write("README.md", "hello")
	write("sub dir/requirements.txt", "flask==3.0\n")
	run("add", "-A")
	run("commit", "-q", "-m", "base")
	base := run("rev-parse", "HEAD")
	want := map[string]bool{
		"README.md":                false,
		"target/out file.class":    true,
		"target/say \"hi\".class":  true,
		"target/tab\there.class":   true,
		"target/naïve.class":       true,
		"src/a b.txt":              false,
		"src/new\nline.txt":        false,
		"src/ünïcode.txt":          false,
		"sub dir/requirements.txt": false,
	}
	for p := range want {
		write(p, "x\n")
	}
	write("sub dir/requirements.txt", "flask==3.0\nrequests==2.32\n")
	run("add", "-A")
	run("add", "-f", "--", "target")
	run("commit", "-q", "-m", "change")
	rev := run("rev-parse", "HEAD")
	tree := t.TempDir()
	if err := gitexec.ExtractCommit(context.Background(), repo, rev, tree); err != nil {
		t.Fatal(err)
	}

	for name, read := range map[string]func() ([]ChangedFile, error){
		"since":   func() ([]ChangedFile, error) { return ChangedFilesSince(t.Context(), repo, base) },
		"between": func() ([]ChangedFile, error) { return ChangedFilesBetween(context.Background(), repo, base, rev, tree) },
		"in hub":  func() ([]ChangedFile, error) { return ChangedFilesInHub(context.Background(), repo, base, rev) },
	} {
		changed, err := read()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := map[string]bool{}
		for _, c := range changed {
			got[c.Path] = c.Ignored
			if c.Path == "sub dir/requirements.txt" && !c.Additive {
				t.Errorf("%s: adding a line to %q is additive", name, c.Path)
			}
		}
		if len(got) != len(want) {
			t.Errorf("%s: changed %v", name, got)
		}
		for p, ignored := range want {
			if v, ok := got[p]; !ok || v != ignored {
				t.Errorf("%s: %q: want ignored=%v, got %v (present %v)", name, p, ignored, v, ok)
			}
		}
	}
}

// Additive means nothing that was there is gone or different: a new line,
// or a new JSON entry that gives the line before it a trailing comma.
func TestAdditiveDiff(t *testing.T) {
	t.Parallel()
	add := "--- a/requirements.txt\n+++ b/requirements.txt\n@@ -3,0 +4 @@\n+whitenoise==6.7.0\n"
	if !additiveDiff(add) {
		t.Fatal("a new line is additive")
	}
	jsonAdd := "@@ -5 +5,2 @@\n-    \"react\": \"18.3.1\"\n+    \"react\": \"18.3.1\",\n+    \"zod\": \"3.23.8\"\n"
	if !additiveDiff(jsonAdd) {
		t.Fatal("appending a JSON entry is additive")
	}
	bump := "@@ -3 +3 @@\n-django==5.0.6\n+django==5.1.1\n"
	if additiveDiff(bump) {
		t.Fatal("a version bump moved a dependency")
	}
	inline := "@@ -5 +5 @@\n-dependencies = [\"django==5.2.17\", \"psycopg[binary]>=3.2\"]\n+dependencies = [\"django==5.2.17\", \"psycopg[binary]>=3.2\", \"whitenoise\"]\n"
	if !additiveDiff(inline) {
		t.Fatal("one more entry on a one-line list is additive")
	}
	inlineBump := "@@ -5 +5 @@\n-dependencies = [\"django==5.2.17\", \"psycopg[binary]>=3.2\"]\n+dependencies = [\"django==5.2.18\", \"psycopg[binary]>=3.2\", \"whitenoise\"]\n"
	if additiveDiff(inlineBump) {
		t.Fatal("a bump hidden in a one-line list still moved a dependency")
	}
	remove := "@@ -3 +2,0 @@\n-django==5.0.6\n"
	if additiveDiff(remove) {
		t.Fatal("a removal moved a dependency")
	}
}

// Every changed file must be named in the report, by path or basename;
// a report artifact the change CREATED is the claim and is not counted,
// but one that was already tracked is the project's file (a build report
// kept at .apptown/REPORT.md) and counts like any other.
func TestUnclaimedChangesAreNamed(t *testing.T) {
	t.Parallel()
	changed := []ChangedFile{{Path: "Dockerfile"}, {Path: ".apptown/app.yaml"}, {Path: "REPORT.md"},
		{Path: "src/main/java/com/example/GreetingController.java"}, {Path: ".apptown/CHANGE.md", Added: true}}
	if got := UnclaimedChanges([]ChangedFile{{Path: ".apptown/REPORT.md"}}, "removed the stale report"); len(got) != 1 {
		t.Fatalf("a rewritten TRACKED report artifact is a change to claim, got %v", got)
	}
	claim := "Added `Dockerfile` and `.apptown/app.yaml`; GreetingController.java serves /healthz."
	got := UnclaimedChanges(changed, claim)
	if len(got) != 1 || got[0] != "REPORT.md" {
		t.Fatalf("want REPORT.md unclaimed, got %v", got)
	}
	if got := UnclaimedChanges(changed, claim+" REPORT.md restored."); len(got) != 0 {
		t.Fatalf("named by basename counts: %v", got)
	}
}

// A generator's tree is claimed by naming its directory: a word ending in
// "/", or a word or path that is a directory of the change ("config",
// "app/models"), claims every changed file under it; a word naming no
// directory of the change ("environments"), a mention of one file, or the
// root claims nothing more, so a top-level file is still named itself.
func TestADirectoryInTheReportClaimsTheFilesUnderIt(t *testing.T) {
	t.Parallel()
	changed := []ChangedFile{{Path: "app/models/book.rb"}, {Path: "app/controllers/books_controller.rb"},
		{Path: "config/routes.rb"}, {Path: "config/environments/production.rb"}, {Path: "bin/rails"}, {Path: "Gemfile"}}
	for _, tc := range []struct {
		claim string
		want  []string
	}{
		{"Ran `rails new`: it generated app/, config/ and bin/. Added the Gemfile.", nil},
		{"Generated ./app/ and ./config/ (rails new); Gemfile pins rails; bin/rails is the binstub.", nil},
		{"The app/models tree holds Book; app/controllers is generated. config/ and bin/ too. Gemfile.", nil},
		{"Ran rails new; the app and config are generated. Gemfile, bin/rails.", nil},
		{"Ran rails new: bin, config and app are generated.", []string{"Gemfile"}},
		{"The generator wrote lib and models; the root . too. Gemfile.", []string{"app/models/book.rb", "app/controllers/books_controller.rb", "config/routes.rb", "config/environments/production.rb", "bin/rails"}},
		{"Changed app/models/book.rb and config/routes.rb. Gemfile, bin/rails.", []string{"app/controllers/books_controller.rb", "config/environments/production.rb"}},
		{"Everything under ./ and / changed. Gemfile, bin/rails.", []string{"app/models/book.rb", "app/controllers/books_controller.rb", "config/routes.rb", "config/environments/production.rb"}},
		{"Changed models/ and environments; Gemfile, bin/rails, app/controllers/, config/routes.rb.", []string{"app/models/book.rb", "config/environments/production.rb"}},
	} {
		if got := UnclaimedChanges(changed, tc.claim); !slices.Equal(got, tc.want) {
			t.Errorf("%q: unclaimed %v, want %v", tc.claim, got, tc.want)
		}
	}
	// A folder claims what is under it, not a top-level file that shares
	// its name's start: rails new's config.ru beside config/.
	if got := UnclaimedChanges([]ChangedFile{{Path: "config/routes.rb"}, {Path: "config.ru"}}, "config is generated"); !slices.Equal(got, []string{"config.ru"}) {
		t.Errorf("config claimed %v unclaimed, want [config.ru]", got)
	}
}
