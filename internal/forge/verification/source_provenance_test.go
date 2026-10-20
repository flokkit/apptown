package verification

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/gitexec/gittest"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
)

// A source scan at the gate holds a change to what it INTRODUCED: findings
// already on the base commit are carried debt — reported, not gating.
// Change 154 on eight-ball is the motivating case:
// four semgrep findings on main for weeks blocked a CSS-and-JS change.
func sarifOf(results ...string) string {
	return `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"semgrep","rules":[` +
		`{"id":"go.math-random","defaultConfiguration":{"level":"warning"}},{"id":"js.eval","defaultConfiguration":{"level":"error"}}]}},` +
		`"results":[` + strings.Join(results, ",") + `]}]}`
}

const (
	oldFinding = `{"ruleId":"go.math-random","level":"warning","message":{"text":"math/rand used"},"partialFingerprints":{"matchBasedId/v1":"aaa111"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"main.go"},"region":{"startLine":8}}}]}`
	// Same rule and file, different line and fingerprint: the loose key still
	// says it is the old finding moved, not a new one.
	oldMoved   = `{"ruleId":"go.math-random","level":"warning","message":{"text":"math/rand used"},"partialFingerprints":{"matchBasedId/v1":"aaa222"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"main.go"},"region":{"startLine":19}}}]}`
	newFinding = `{"ruleId":"js.eval","level":"error","message":{"text":"eval() used"},"partialFingerprints":{"matchBasedId/v1":"bbb111"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"web/ball.js"},"region":{"startLine":3}}}]}`
)

// provenanceReports selects evidence from the actual staged tree: only the
// change contains web/ball.js. Report writing uses the existing scanner fake.
type provenanceReports struct {
	scannerEffects
	base string
}

func (e *provenanceReports) Run(ctx context.Context, req execution.Request) execution.Outcome {
	e.t.Helper()
	report := &scannerEffects{t: e.t, report: e.report}
	if _, err := os.Stat(filepath.Join(req.Workspace, "web", "ball.js")); os.IsNotExist(err) {
		report.report = e.base
	} else if err != nil {
		e.t.Fatal(err)
	}
	return report.Run(context.Background(), req)
}

func TestScanGateHoldsOnlyIntroducedFindings(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	if _, err := integration.New(db).Upsert(t.Context(), "semgrep", ScannerCapability, "tool",
		`{"image":"semgrep/semgrep:test","command":["semgrep","/work"],"report":".apptown/semgrep.sarif","format":"sarif","target":"source","kind":"sast"}`, true); err != nil {
		t.Fatal(err)
	}
	reports := &provenanceReports{scannerEffects: scannerEffects{t: t}, base: sarifOf(oldFinding)}
	// This source comparison uses only bindings and evaluator execution.
	scanners := NewScanners(ScannerDependencies{DB: db, Evaluator: NewEvaluator(reports, nil)})

	ws := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := gittest.Command(args...)
		cmd.Dir = ws
		cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(ws, "main.go"), []byte("package main // math/rand"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "seed")
	baseSHA := git("rev-parse", "HEAD")
	if err := os.Mkdir(filepath.Join(ws, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "web", "ball.js"), []byte("eval('x')"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "the change")

	// The base has the old finding; the change has it (moved) plus a new one.
	reports.report = sarifOf(oldMoved, newFinding)
	src, cleanup, err := scm.StageTreeAt(context.Background(), ws, strings.TrimSpace(git("rev-parse", "HEAD")))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	results, findings, errs := scanners.RunSourceScans(context.Background(), src, "test", ws, baseSHA)
	if errs != 0 {
		t.Fatalf("scan errors: %d", errs)
	}
	if len(findings) != 1 || findings[0].Identifiers[0] != "js.eval" {
		t.Fatalf("only the introduced finding should gate, got %+v", findings)
	}
	var semgrep workflow.CheckReceipt
	for _, r := range results {
		if r.Check == "semgrep" {
			semgrep = r
		}
	}
	if semgrep.Verdict != workflow.VerdictFail || semgrep.Carried != 1 {
		t.Fatalf("the moved old finding is carried (1), the new one fails the check: %+v", semgrep)
	}
	if note := semgrep.CarriedNote; !strings.Contains(note, "main.go") {
		t.Fatalf("carried debt must be named on the result: %+v", semgrep)
	}

	// Same change, nothing new: the scan passes and says what it is carrying.
	reports.report = sarifOf(oldMoved)
	results, findings, errs = scanners.RunSourceScans(context.Background(), src, "test", ws, baseSHA)
	if errs != 0 || len(results) != 1 {
		t.Fatalf("carried-only scan: %d errors, results %+v", errs, results)
	}
	if len(findings) != 0 {
		t.Fatalf("carried debt alone must not gate: %+v", findings)
	}
	if r := results[0]; r.Check != "semgrep" || r.Verdict != workflow.VerdictPass || r.Carried != 1 {
		t.Fatalf("expected semgrep pass with 1 carried, got %+v", r)
	}

	// No base to compare with: every finding gates.
	reports.report = sarifOf(oldMoved, newFinding)
	_, findings, errs = scanners.RunSourceScans(context.Background(), src, "test", ws, "")
	if errs != 0 || len(findings) != 2 {
		t.Fatalf("without a base every finding is introduced, got %d findings and %d errors", len(findings), errs)
	}
}
