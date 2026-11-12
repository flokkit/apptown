package verification

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/fix"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/stock"
)

func routineGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	argv := append([]string{"-C", repo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.test"}, args...)
	out, err := gittest.Command(argv...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func routineWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
func routineRepository(t *testing.T) (string, string, workflow.Source) {
	t.Helper()
	data := t.TempDir()
	source := workflow.Source{ProjectID: idstest.N(1), ChangeID: idstest.N(2), RunID: idstest.N(3), RootRunID: idstest.N(3), Branch: "change", DefaultBranch: "main", Slug: "example"}
	repo, err := source.Repository(data)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(repo, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	routineGit(t, repo, "init", "-b", "main")
	routineWrite(t, filepath.Join(repo, ".apptown/app.yaml"), []byte("app:\n  web:\n    build: .\n    public: true\n    ports: [\"8080\"]\n    tools: [node]\n"))
	for _, version := range []string{"1.0.0", "1.0.1"} {
		s := routineFixture(version)
		routineWrite(t, filepath.Join(repo, "package.json"), s.Manifest)
		routineWrite(t, filepath.Join(repo, "package-lock.json"), s.Lock)
		routineGit(t, repo, "add", ".")
		routineGit(t, repo, "commit", "-m", version)
		if version == "1.0.0" {
			source.ChangeBaseSHA = routineGit(t, repo, "rev-parse", "HEAD")
			routineGit(t, repo, "checkout", "-b", "change")
		}
	}
	return data, repo, source
}
func routineExecutor(t *testing.T, after func()) Executor {
	t.Helper()
	return Executor{Local: true, AgentImage: "test-agent", Image: func(context.Context, execution.ImageRequest) execution.ImageAnswer {
		return execution.ImageAnswer{ID: "sha256:" + strings.Repeat("a", 64)}
	}, Run: func(_ context.Context, req execution.Request) execution.Outcome {
		if req.Report == ".apptown-update-list.jsonl" {
			if _, err := os.Stat(filepath.Join(req.Workspace, "npm-shrinkwrap.json")); err == nil {
				return execution.Outcome{Termination: execution.Exited, ExitCode: 1}
			}
			raw, err := os.ReadFile(filepath.Join(req.Workspace, "package-lock.json"))
			if err != nil {
				t.Fatal(err)
			}
			var lock struct {
				Packages map[string]struct {
					Version string `json:"version"`
				}
			}
			if err := json.Unmarshal(raw, &lock); err != nil {
				t.Fatal(err)
			}
			rows := []byte{}
			for path, p := range lock.Packages {
				if path == "" {
					continue
				}
				row, err := json.Marshal(fix.Listing{Ecosystem: "npm", Package: strings.TrimPrefix(path, "node_modules/"), Version: p.Version, Source: "https://registry.npmjs.org/example", Attributes: map[string]any{}})
				if err != nil {
					t.Fatal(err)
				}
				rows = append(rows, append(row, '\n')...)
			}
			if after != nil {
				after()
			}
			return execution.Outcome{Termination: execution.Exited, ExitCode: 0, Report: rows}
		}
		t.Fatalf("unexpected listing request: %+v", req)
		return execution.Outcome{}
	}}
}
func TestRoutineInspectionUsesTheCompleteDiffAndCaughtUpBase(t *testing.T) {
	data, repo, source := routineRepository(t)
	routineGit(t, repo, "checkout", "main")
	routineWrite(t, filepath.Join(repo, "README"), []byte("new main content\n"))
	routineGit(t, repo, "add", ".")
	routineGit(t, repo, "commit", "-m", "main progressed")
	main := routineGit(t, repo, "rev-parse", "HEAD")
	routineGit(t, repo, "checkout", "change")
	routineGit(t, repo, "merge", "main", "--no-edit")
	inspector := testRoutineInspector(t, data, &scm.RepositoryLocks{}, routineExecutor(t, nil))
	result, err := inspector.InspectRoutine(t.Context(), source, workflow.StandingCategories{Dependencies: "routine"})
	if err != nil || result.Classification != "routine-dependency" || result.Base != main || len(result.ChangedFiles) != 2 {
		t.Fatalf("wrong caught-up source: %+v %v", result, err)
	}
	routineWrite(t, filepath.Join(repo, "unrelated.txt"), []byte("code change\n"))
	routineGit(t, repo, "add", ".")
	routineGit(t, repo, "commit", "-m", "mixed change")

	result, err = inspector.InspectRoutine(t.Context(), source, workflow.StandingCategories{Dependencies: "routine"})
	if err != nil || result.Classification != "routine-dependency" || len(result.ChangedFiles) != 3 {
		t.Fatalf("mixed diff concealed: %+v %v", result, err)
	}
}
func TestRoutineInspectionRefusesSourceMovementAndDirtyWorkspace(t *testing.T) {
	data, repo, source := routineRepository(t)
	moved := false
	inspector := testRoutineInspector(t, data, &scm.RepositoryLocks{}, routineExecutor(t, func() {
		if moved {
			return
		}
		moved = true
		routineWrite(t, filepath.Join(repo, "moved.txt"), []byte("new work"))
		routineGit(t, repo, "add", ".")
		routineGit(t, repo, "commit", "-m", "moved")
	}))
	result, err := inspector.InspectRoutine(t.Context(), source, workflow.StandingCategories{Dependencies: "routine"})
	if err != nil || result.Classification != "cannot-assess" || !strings.Contains(result.Reason, "moved") {
		t.Fatalf("moved source qualified: %+v %v", result, err)
	}
	// Return to the published pure candidate, then create its own local checkout.
	routineGit(t, repo, "reset", "--hard", "HEAD~1")
	workspace, err := source.Workspace(data)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(workspace), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err = (workflow.GitWorkspace{DataDir: data}).EnsureClone(t.Context(), workflow.CloneRequest{Repository: repo, Workspace: workspace, Branch: source.Branch, DefaultBranch: source.DefaultBranch}); err != nil {
		t.Fatal(err)
	}
	routineWrite(t, filepath.Join(workspace, "dirty.txt"), []byte("unsaved work"))
	inspector.executor.Run = func(context.Context, execution.Request) execution.Outcome {
		t.Fatal("dirty source executed validator")
		return execution.Outcome{}
	}
	result, err = inspector.InspectRoutine(t.Context(), source, workflow.StandingCategories{Dependencies: "routine"})
	if err != nil || result.Classification != "cannot-assess" || !strings.Contains(result.Reason, "dirty") {
		t.Fatalf("dirty source qualified: %+v %v", result, err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "dirty.txt"))
	if err != nil || string(raw) != "unsaved work" {
		t.Fatalf("inspection modified source: %q %v", raw, err)
	}
}

func TestRoutineRefusesUnchangedShrinkwrap(t *testing.T) {
	data, repo, source := routineRepository(t)
	routineGit(t, repo, "checkout", "main")
	routineWrite(t, filepath.Join(repo, "npm-shrinkwrap.json"), routineFixture("1.0.0").Lock)
	routineGit(t, repo, "add", ".")
	routineGit(t, repo, "commit", "-m", "existing shrinkwrap")
	routineGit(t, repo, "checkout", "change")
	routineGit(t, repo, "merge", "main", "--no-edit")
	calls := 0
	inspector := testRoutineInspector(t, data, &scm.RepositoryLocks{}, routineExecutor(t, func() { calls++ }))
	result, err := inspector.InspectRoutine(t.Context(), source, workflow.StandingCategories{Dependencies: "routine"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Classification != "cannot-assess" || calls != 0 {
		t.Fatalf("unsupported shrinkwrap must refuse before validation: classification=%s calls=%d", result.Classification, calls)
	}
}

func TestRoutineReportsAllMixedFiles(t *testing.T) {
	data, repo, source := routineRepository(t)
	routineWrite(t, filepath.Join(repo, "README"), []byte("mixed edit"))
	routineGit(t, repo, "add", ".")
	routineGit(t, repo, "commit", "-m", "mixed source")
	inspector := testRoutineInspector(t, data, &scm.RepositoryLocks{}, routineExecutor(t, nil))
	result, err := inspector.InspectRoutine(t.Context(), source, workflow.StandingCategories{Dependencies: "routine"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Classification != "routine-dependency" || len(result.ChangedFiles) != 3 {
		t.Fatalf("incomplete mixed inventory: classification=%s files=%v", result.Classification, result.ChangedFiles)
	}
}

func TestRoutineEmptySourceRequiresSeparatelyCheckedAdoption(t *testing.T) {
	data, repo, source := routineRepository(t)
	routineGit(t, repo, "reset", "--hard", source.ChangeBaseSHA)
	var locks scm.RepositoryLocks
	inspector := testRoutineInspector(t, data, &locks, Executor{Run: func(context.Context, execution.Request) execution.Outcome {
		t.Fatal("empty source ran validation container")
		return execution.Outcome{}
	}})
	ordinary, err := inspector.InspectRoutine(t.Context(), source, workflow.StandingCategories{WaysOfWorking: true})
	if err != nil || ordinary.Classification != "requires-person" {
		t.Fatalf("empty source became sufficient: %+v %v", ordinary, err)
	}
	adoption, err := inspector.InspectRoutineWithAdoption(t.Context(), source, workflow.StandingCategories{WaysOfWorking: true})
	if err != nil || adoption.Classification != "routine-adoption" || len(adoption.ChangedFiles) != 0 || adoption.Candidate == "" {
		t.Fatalf("exact empty Git observation: %+v %v", adoption, err)
	}
	routineWrite(t, filepath.Join(repo, "unexpected.txt"), []byte("unclassified change"))
	routineGit(t, repo, "add", ".")
	routineGit(t, repo, "commit", "-m", "unrelated")
	mixed, err := inspector.InspectRoutineWithAdoption(t.Context(), source, workflow.StandingCategories{WaysOfWorking: true})
	if err != nil || mixed.Classification != "routine-adoption" || len(mixed.ChangedFiles) != 1 {
		t.Fatalf("adoption erased unrelated source: %+v %v", mixed, err)
	}
}

func testRoutineInspector(t *testing.T, data string, locks *scm.RepositoryLocks, executor Executor) routineInspector {
	t.Helper()
	r := NewRoutineInspector(data, locks, executor)
	r.db = testDB(t)
	blocks, skipped := wow.ReadStockGuidance(stock.FS, nil)
	if skipped != 0 {
		t.Fatal(skipped)
	}
	for _, b := range blocks {
		if b.Name == "lang/node/dependency-updates" {
			ds, err := fix.Parse(b.FixesJSON)
			if err != nil {
				t.Fatal(err)
			}
			r.fixes = func(context.Context, workflow.Source) ([]fix.Declaration, []check.Finding, error) {
				return ds, nil, nil
			}
			return r
		}
	}
	t.Fatal("stock node fix missing")
	return r
}

type npmFixture struct{ Manifest, Lock []byte }

func routineFixture(version string) npmFixture {
	return npmFixture{Manifest: []byte(fmt.Sprintf(`{"name":"app","dependencies":{"example":"%s"}}`, version)), Lock: []byte(fmt.Sprintf(`{"name":"app","lockfileVersion":3,"packages":{"":{"name":"app","dependencies":{"example":"%s"}},"node_modules/example":{"version":"%s","resolved":"https://registry.npmjs.org/example/-/example-%s.tgz","integrity":"sha512-%s"}}}`, version, version, version, base64.StdEncoding.EncodeToString(make([]byte, 64))))}
}

func TestSecurityAssessmentUsesDeclaredListingsAndRecordedFixedTargets(t *testing.T) {
	data, _, source := routineRepository(t)
	inspector := testRoutineInspector(t, data, &scm.RepositoryLocks{}, routineExecutor(t, nil))
	declarations := inspector.fixes
	inspector.fixes = func(ctx context.Context, source workflow.Source) ([]fix.Declaration, []check.Finding, error) {
		ds, _, err := declarations(ctx, source)
		return ds, []check.Finding{{Package: &check.PackageRef{Name: "example", Ecosystem: "npm", FixedIn: "1.0.2"}}}, err
	}
	result, err := inspector.InspectRoutine(t.Context(), source, workflow.StandingCategories{SecurityFixes: true})
	if err != nil || result.Classification != "requires-person" || !strings.Contains(result.Reason, "example") || !strings.Contains(result.Reason, "FixedIn") {
		t.Fatalf("below fixed version: %+v %v", result, err)
	}
	inspector.fixes = func(ctx context.Context, source workflow.Source) ([]fix.Declaration, []check.Finding, error) {
		ds, _, err := declarations(ctx, source)
		return ds, []check.Finding{{Package: &check.PackageRef{Name: "example", Ecosystem: "npm", FixedIn: "1.0.1"}}}, err
	}
	result, err = inspector.InspectRoutine(t.Context(), source, workflow.StandingCategories{SecurityFixes: true})
	if err != nil || result.Classification != "routine-dependency" {
		t.Fatalf("fixed security target: %+v %v", result, err)
	}
}

func TestUpdateLevelsBoundMajorsAndProtectBuildInputs(t *testing.T) {
	for _, level := range []string{"routine", "aggressive"} {
		for _, change := range []string{"major", "root tool", "part tool", "base image", "contract image"} {
			t.Run(level+"/"+change, func(t *testing.T) {
				data, repo, source := routineRepository(t)
				files := map[string]string{
					"major": "package-lock.json", "root tool": "mise.toml", "part tool": "web/mise.toml", "base image": "Dockerfile", "contract image": ".apptown/app.yaml",
				}
				path := filepath.Join(repo, files[change])
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				raw := []byte("[tools]\nnode = '26'\n")
				switch change {
				case "major":
					raw = routineFixture("2.0.0").Lock
				case "base image":
					raw = []byte("FROM node:26\n")
				case "contract image":
					raw = []byte("app:\n  web:\n    image: node:26\n    public: true\n")
				}
				routineWrite(t, path, raw)
				routineGit(t, repo, "add", ".")
				routineGit(t, repo, "commit", "-m", change)
				inspector := testRoutineInspector(t, data, &scm.RepositoryLocks{}, routineExecutor(t, nil))
				got, err := inspector.InspectRoutine(t.Context(), source, workflow.StandingCategories{Dependencies: level})
				want := "requires-person"
				if change == "major" && level == "aggressive" {
					want = "routine-dependency"
				}
				if err != nil || got.Classification != want {
					t.Fatalf("%+v %v; want %s", got, err, want)
				}
			})
		}
	}
}
