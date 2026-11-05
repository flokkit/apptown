package check_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/check"
)

// The evaluator identity changes with the rubric, its files, or the
// packing/template — and is stable otherwise.
func TestLLMEvaluatorIdentity(t *testing.T) {
	t.Parallel()
	c := check.Declaration{AppliesTo: repoWide, Name: "r", Evaluator: "llm", Scope: "tree", Rubric: "plain", Files: []string{"README.md"}}
	base := check.LLMEvaluatorVersion(c)
	if !strings.HasPrefix(base, "llm/"+check.PackingVersion+"/"+check.TemplateHash()+"/") {
		t.Fatalf("identity shape: %s", base)
	}
	if check.LLMEvaluatorVersion(c) != base {
		t.Fatal("identity must be stable for the same check")
	}
	c2 := c
	c2.Rubric = "plainer"
	c3 := c
	c3.Files = []string{"README.md", "docs/*.md"}
	if check.LLMEvaluatorVersion(c2) == base || check.LLMEvaluatorVersion(c3) == base {
		t.Fatal("a changed rubric or file list is a changed evaluator")
	}
	// Packing is deterministic: same tree, same bundle hash; different tree, different hash.
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "one")
	writeFile(t, dir, "b/README.md", "two")
	b1, files, _, _ := check.PackEvidence(check.Source{Tree: dir}, check.Declaration{Files: []string{"**/README.md"}})
	b2, _, _, _ := check.PackEvidence(check.Source{Tree: dir}, check.Declaration{Files: []string{"**/README.md"}})
	if b1 != b2 || len(files) != 2 || files[0] != "README.md" || files[1] != "b/README.md" {
		t.Fatalf("packing must be deterministic and sorted: %v", files)
	}
	writeFile(t, dir, "README.md", "changed")
	if b3, _, _, _ := check.PackEvidence(check.Source{Tree: dir}, check.Declaration{Files: []string{"**/README.md"}}); check.BundleHash(b3) == check.BundleHash(b1) {
		t.Fatal("changed evidence must change the bundle hash")
	}
}

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A check author's glob means what it says anywhere in the pattern: "**" spans
// any depth at the start, middle or end, and "*" stays within one directory.
func TestCheckFileGlobsSpanDirectoriesOnlyWhereTheySayTo(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"**/*.go", "main.go", true},
		{"**/*.go", "a/b/main.go", true},
		{"src/**/*.go", "src/a/b/c.go", true},
		{"src/**/*.go", "src/c.go", true},
		{"src/**/*.go", "lib/c.go", false},
		{"docs/**", "docs/a/b.md", true},
		{"docs/*.md", "docs/a/b.md", false},
		{"Dockerfile*", "Dockerfile.prod", true},
		{"[", "[", false},
	}
	for _, c := range cases {
		if got := check.GlobMatch(c.pattern, c.path); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// A file a check names by its exact path is evidence even inside a directory
// the walk skips: the runtime-contract check names .apptown/app.yaml, and a
// committed contract must not read as missing. AppTown's own runtime state
// beside it stays out, and a symlinked directory never leads outside the tree.
func TestNamedFilesAreEvidenceEvenUnderSkippedDirectories(t *testing.T) {
	t.Parallel()
	globs := []string{"README.md", ".apptown/app.yaml", ".apptown/compose.yaml", "**/*.md", "**/*.yaml"}

	without := t.TempDir()
	writeFile(t, without, "README.md", "an app")
	writeFile(t, without, ".apptown/CHANGE.md", "the implementer's report")
	writeFile(t, without, ".apptown/sessions/s.md", "a transcript")
	if bundle, files, _, _ := check.PackEvidence(check.Source{Tree: without}, check.Declaration{Files: globs}); len(files) != 1 || files[0] != "README.md" || strings.Contains(bundle, "transcript") || strings.Contains(bundle, "report") {
		t.Fatalf("without a contract only README.md is evidence, got %v\n%s", files, bundle)
	}

	with := t.TempDir()
	writeFile(t, with, "README.md", "an app")
	writeFile(t, with, ".apptown/app.yaml", "app:\n  web: { build: ., public: true }\n")
	writeFile(t, with, ".apptown/CHANGE.md", "the implementer's report")
	bundle, files, _, _ := check.PackEvidence(check.Source{Tree: with}, check.Declaration{Files: globs})
	if strings.Join(files, ",") != ".apptown/app.yaml,README.md" || !strings.Contains(bundle, "=== .apptown/app.yaml ===\napp:") {
		t.Fatalf("a committed contract is evidence, got %v\n%s", files, bundle)
	}

	outside := t.TempDir()
	writeFile(t, outside, "app.yaml", "secret: outside the tree")
	linked := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(linked, ".apptown")); err != nil {
		t.Fatal(err)
	}
	if bundle, files, _, _ := check.PackEvidence(check.Source{Tree: linked}, check.Declaration{Files: globs}); len(files) != 0 || strings.Contains(bundle, "outside") {
		t.Fatalf("a symlinked directory must not lead evidence outside the tree, got %v", files)
	}
}

// A rubric is judged on all of its evidence or not at all: evidence AppTown
// could not read makes the item cannot-assess, and the model is never asked
// about a bundle that silently lacks it. Other items are still asked.
//
// Kills: the walk and read errors skipped in PackEvidence (fallback audit
// F14), which sent a plausible partial bundle.
func TestARubricWhoseEvidenceCannotBeReadIsNotJudged(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("# readable\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A path Git could hold but the file system cannot name (one component
	// over the 255-byte limit): reading it fails whoever runs the test.
	unreadable := strings.Repeat("x", 300) + ".md"
	rubric := func(name string, files ...string) check.Declared {
		return check.Declared{Identity: check.Identity{Namespace: "b", Version: 1},
			Declaration: check.Declaration{AppliesTo: repoWide, Name: name, Evaluator: "llm", Scope: "tree", Rubric: "judge", Expects: "it holds", Files: files}}
	}
	items := check.TreeItems([]check.Declared{rubric("partial", "README.md", unreadable), rubric("whole", "README.md")}, check.Source{Tree: src})
	if len(items) != 2 || items[0].Unreadable == "" || items[1].Unreadable != "" {
		t.Fatalf("items: %+v", items)
	}
	var asked string
	j := &check.Judge{ShImage: testShImage, Complete: check.CompleterFunc(func(_ context.Context, _, user string) (check.Completion, error) {
		asked = user
		return check.Completion{Text: `[{"item":1,"verdict":"pass"},{"item":2,"verdict":"pass"}]`}, nil
	})}
	verdicts, _, err := j.Checklist(context.Background(), items, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(asked, "b v1 · partial") || !strings.Contains(asked, "b v1 · whole") {
		t.Fatalf("the prompt:\n%s", asked)
	}
	for i, it := range items {
		v, answered := verdicts[i+1]
		ev := check.Judgement(it, v, answered, nil, check.Gate)
		want := check.Conforms
		if i == 0 {
			want = check.CannotAssess
		}
		if ev.Outcome != want {
			t.Fatalf("%s: %s (%s), want %s", it.Name, ev.Outcome, ev.Detail, want)
		}
	}

	// A directory the walk cannot read is the same, for a reader it is
	// closed to (root reads it anyway, so the case says nothing as root).
	if os.Geteuid() != 0 {
		closed := filepath.Join(src, "docs")
		if err := os.Mkdir(closed, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(closed, 0o755) })
		if _, _, _, err := check.PackEvidence(check.Source{Tree: src}, check.Declaration{Files: []string{"**/*.md"}}); err == nil {
			t.Fatal("an unreadable directory was left out of the evidence")
		}
	}
}

// The exact state-behind-needs globs collect app code at every depth, even
// when the caller reaches the checked tree through a directory symlink.
func TestStateRubricCollectsAppTree(t *testing.T) {
	t.Parallel()
	globs := []string{".apptown/app.yaml", "go.mod", "requirements*.txt", "pyproject.toml", "package.json", "Gemfile", "**/settings.py", "config/database.yml", "**/*.go", "**/*.py", "**/*.rb", "**/*.ts", "**/*.js"}
	dir := t.TempDir()
	want := []string{".apptown/app.yaml", "go.mod", "requirements-dev.txt", "pyproject.toml", "package.json", "Gemfile", "settings.py", "config/database.yml", "main.go", "src/main.ts", "src/lib/store.js", "app/models/record.rb", "app/api/nested/store.py"}
	for _, rel := range want {
		writeFile(t, dir, rel, "evidence for "+rel)
	}
	for _, rel := range []string{"node_modules/client.js", "vendor/client.go", "dist/main.js", "build/main.js", ".venv/client.py", "__pycache__/client.py", ".apptown/sessions/code.py", ".forge/code.go", ".next/code.js", "target/code.go", ".git/code.go", "src/client.min.js", "src/client.js.map"} {
		writeFile(t, dir, rel, "excluded")
	}
	writeFile(t, dir, "src/binary.go", "binary\x00")
	outside := t.TempDir()
	writeFile(t, outside, "outside.py", "outside tree")
	if err := os.Symlink(outside, filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "tree")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	slices.Sort(want)
	for _, src := range []string{dir, link} {
		items := check.TreeItems([]check.Declared{{Declaration: check.Declaration{Evaluator: "llm", Scope: "tree", Rubric: "state", Files: globs}}}, check.Source{Tree: src})
		if len(items) != 1 || items[0].Unreadable != "" || !slices.Equal(items[0].Files, want) {
			t.Fatalf("tree %s: got %+v, want %v", src, items, want)
		}
	}
}

// Packing keeps a sorted prefix within both existing budgets and distinguishes
// excluded files from text evidence omitted by a budget.
func TestTreeEvidenceBudgetsKeepMatchingCounts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go"} {
		writeFile(t, dir, name, strings.Repeat("x", 20*1024))
	}
	writeFile(t, dir, "z.go", "binary\x00")
	var checks []check.Declared
	for i := 0; i < 6; i++ {
		checks = append(checks, check.Declared{Declaration: check.Declaration{Evaluator: "llm", Scope: "tree", Files: []string{"**/*.go"}}})
	}
	items := check.TreeItems(checks, check.Source{Tree: dir})
	total := 0
	omitted := false
	for _, it := range items {
		total += len(it.Evidence)
		if it.MatchingFiles != 6 {
			t.Fatalf("eligible matches: %d, want 6", it.MatchingFiles)
		}
		if len(it.Evidence) > 48*1024 {
			t.Fatalf("rubric exceeds budget: %d", len(it.Evidence))
		}
		if len(it.Files) == 0 {
			omitted = true
			if !strings.Contains(it.Evidence, "project bundle cap") {
				t.Fatalf("missing batch omission: %s", it.Evidence)
			}
		} else {
			if !slices.Equal(it.Files, []string{"a.go", "b.go", "c.go"}) || !strings.Contains(it.Evidence, "3 more matching file(s) omitted") || !strings.Contains(it.Evidence, "truncated: 8192 more bytes") {
				t.Fatalf("capped evidence: %v, %s", it.Files, it.Evidence)
			}
		}
	}
	if total > 160*1024 || !omitted {
		t.Fatalf("batch budget: bytes=%d, omitted=%v", total, omitted)
	}
}

// Supplied verdicts cannot promote project-authored assertions to evidence.
func TestJudgementRequiresEvidenceBeyondProjectClaims(t *testing.T) {
	for _, citation := range []string{"PROJECT-NOTES.md:2", "AGENTS.md:7", "./PROJECT-NOTES.md:2 and docs/AGENTS.md:3"} {
		for _, verdict := range []string{"pass", "fail", "unclear"} {
			it := check.Item{}
			got := check.Judgement(it, check.Verdict{Verdict: verdict, Evidence: citation}, true, nil, check.Gate)
			if got.Outcome != check.CannotAssess || len(got.Findings) != 0 {
				t.Fatalf("%s %s: %+v", verdict, citation, got)
			}
		}
	}
	for _, tc := range []struct{ use, evidence string }{{"", "PROJECT-NOTES.md:2 and app.go:3"}, {"wording", "PROJECT-NOTES.md:2"}} {
		it := check.Item{Declared: check.Declared{Declaration: check.Declaration{EvidenceUse: tc.use}}}
		if got := check.Judgement(it, check.Verdict{Verdict: "pass", Evidence: tc.evidence}, true, nil, check.Gate); got.Outcome != check.Conforms {
			t.Fatalf("%+v: %+v", tc, got)
		}
	}
}
