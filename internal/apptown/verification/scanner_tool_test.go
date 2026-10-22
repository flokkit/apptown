package verification

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
)

func TestRunToolChecksSourceFailAndPass(t *testing.T) {
	t.Parallel()
	f := newScannerOwnerFixture(t)
	f.semgrep(t)
	sc := []conformance.DeclaredCheck{{Block: "lang/django", Version: 3,
		Check: conformance.CheckDeclaration{Name: "python-sast", Evaluator: "tool", Tool: "semgrep-django", Severity: "medium", Expects: "no sast findings"}}}

	// Report with a finding → fail, one Finding at the CHECK's severity.
	f.Effects.report = scannerSARIF(1)
	src := t.TempDir()
	results, findings, errs := f.Scanners.RunToolChecks(t.Context(), sc, ToolContext{Context: f.Context, ProjectID: f.ProjectID, BuildDir: src}, "test")
	if errs != 0 {
		t.Fatalf("unexpected tool errors: %d", errs)
	}
	if len(findings) != 1 || findings[0].Severity != "medium" {
		t.Fatalf("want 1 finding at medium, got %+v", findings)
	}
	if !strings.Contains(findings[0].Fingerprint, "lang/django@3") || !strings.Contains(findings[0].Fingerprint, "python-sast") {
		t.Fatalf("finding fingerprint lost provenance: %q", findings[0].Fingerprint)
	}
	if len(results) != 1 || results[0].Verdict != workflow.VerdictFail {
		t.Fatalf("want fail verdict, got %+v", results)
	}

	// Empty report → pass, no findings.
	f.Effects.report = scannerSARIF(0)
	src2 := t.TempDir()
	results, findings, errs = f.Scanners.RunToolChecks(t.Context(), sc, ToolContext{Context: f.Context, ProjectID: f.ProjectID, BuildDir: src2}, "test")
	if len(findings) != 0 || errs != 0 || results[0].Verdict != workflow.VerdictPass {
		t.Fatalf("want clean pass, got results=%+v findings=%+v errs=%d", results, findings, errs)
	}
}

// A tool check whose findings are ALL covered by an active disposition passes —
// conformance honors the same "actionable = findings − dispositions" the scan
// gates use, so a VERIFIED false positive (or accepted exception) clears a
// conformance check, not only a land gate (design/subsystems/findings.md §3).
func TestRunToolChecksHonorsDispositions(t *testing.T) {
	t.Parallel()
	f := newScannerOwnerFixture(t)
	f.semgrep(t)
	pid := f.ProjectID
	sc := []conformance.DeclaredCheck{{Block: "lang/django", Version: 3,
		Check: conformance.CheckDeclaration{Name: "python-sast", Evaluator: "tool", Tool: "semgrep-django", Severity: "medium", Expects: "no sast findings"}}}

	// Finding present, no disposition → fail.
	f.Effects.report = scannerSARIF(1)
	results, _, _ := f.Scanners.RunToolChecks(t.Context(), sc, ToolContext{Context: f.Context, ProjectID: pid, BuildDir: t.TempDir()}, "test")
	if results[0].Verdict != workflow.VerdictFail {
		t.Fatalf("want fail without a disposition, got %+v", results)
	}

	// Failed exception storage is an evaluation error, not an ordinary finding.
	t.Cleanup(f.DB.FailNextStatement("FROM dispositions"))
	failed, _, nerrors := f.Scanners.RunToolChecks(t.Context(), sc, ToolContext{Context: f.Context, ProjectID: pid, BuildDir: t.TempDir()}, "test")
	if len(failed) != 1 || failed[0].Verdict != workflow.VerdictError || nerrors != 1 || !strings.Contains(failed[0].Output, "reading exceptions") {
		t.Fatalf("failed exception read was hidden: %+v errors=%d", failed, nerrors)
	}

	// Accept the finding's rule as a verified false positive → the same finding
	// now clears the check.

	if _, err := f.Conformance.CreateDisposition(f.Context, conformance.ExceptionRequest{ProjectID: pid, Identifier: "django.security.audit.sql-injection", Kind: conformance.FalsePositive, Justification: "verified: parameterized query, no user input reaches SQL"}); err != nil {
		t.Fatal(err)
	}
	f.Effects.report = scannerSARIF(1)
	results, findings, _ := f.Scanners.RunToolChecks(t.Context(), sc, ToolContext{Context: f.Context, ProjectID: pid, BuildDir: t.TempDir()}, "test")
	// EXCEPTED, not pass (design/subsystems/conformance-surface.md §9): the findings stay
	// underneath in the detail and the exception is named — never folded green.
	if results[0].Verdict != workflow.VerdictExcepted {
		t.Fatalf("a fully-dispositioned tool check must be excepted, got %+v", results)
	}
	if eb := results[0].ExceptedBy; !strings.Contains(eb, "false_positive") {
		t.Fatalf("excepted verdict must name the covering exception, got %+v", results[0])
	}
	if len(findings) != 0 {
		t.Fatalf("dispositioned findings must not be actionable, got %+v", findings)
	}
	// An identifier exception names the rule, not the block version: the
	// check a new version of the block declares is covered too, which is
	// what a what-if on that version says.
	moved := []conformance.DeclaredCheck{{Block: sc[0].Block, Version: sc[0].Version + 1, Check: sc[0].Check}}
	f.Effects.report = scannerSARIF(1)
	if results, _, _ := f.Scanners.RunToolChecks(t.Context(), moved, ToolContext{Context: f.Context, ProjectID: pid, BuildDir: t.TempDir()}, "test"); results[0].Verdict != workflow.VerdictExcepted {
		t.Fatalf("an identifier exception covers the check at a new block version, got %+v", results)
	}
}

// The shared build: apptown check / the gate reuse a cached image (content-
// addressed by commit) instead of rebuilding, and a nonzero self-check exit
// becomes a fail at the check's severity.
func TestRunToolChecksExecImageSharedBuild(t *testing.T) {
	t.Parallel()
	f := newScannerOwnerFixture(t)
	f.bind(t, "django-check-deploy", map[string]any{
		"command": []string{"python", "manage.py", "check", "--deploy"},
		"target":  "exec-image", "manual": true,
	})
	// A build-based contract so appImageForCommit reaches the image branch.
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".apptown", "app.yaml"),
		[]byte("app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The daemon reports a cached image. Only the in-image command fails.
	exitOne := exitStatusOne()
	if exitOne == nil {
		t.Fatal("exit fixture unexpectedly succeeded")
	}
	f.Daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "run" {
			return []byte("?: (security.W004) SECURE_HSTS_SECONDS not set"), exitOne, true
		}
		return nil, nil, false
	})

	sc := []conformance.DeclaredCheck{{Block: "lang/django", Version: 5,
		Check: conformance.CheckDeclaration{Name: "django-check-deploy", Evaluator: "tool", Tool: "django-check-deploy", Severity: "high", Expects: "deploy checks pass"}}}
	results, findings, errs := f.Scanners.RunToolChecks(t.Context(), sc,
		ToolContext{Context: f.Context, ProjectID: f.ProjectID, SHA: "abcdef1234567890", BuildDir: src, AllowBuild: true}, "test")

	if builds := f.Daemon.Matching("build "); len(builds) != 0 {
		t.Fatal("exec-image tool rebuilt the app image instead of reusing the cached (shared) one")
	}
	if errs != 0 || len(findings) != 1 || findings[0].Severity != "high" {
		t.Fatalf("want 1 high finding, no errors; got findings=%+v errs=%d", findings, errs)
	}
	if results[0].Verdict != workflow.VerdictFail {
		t.Fatalf("want fail, got %+v", results)
	}
}

// A crashed exec-image tool (traceback / import error) is an ERROR verdict, not
// a conformance FAIL the agent is wrongly told to "fix".
func TestRunToolChecksExecImageCrashIsError(t *testing.T) {
	t.Parallel()
	f := newScannerOwnerFixture(t)
	f.bind(t, "django-check-deploy", map[string]any{
		"command": []string{"sh", "-c", "python manage.py check --deploy"},
		"target":  "exec-image", "manual": true,
	})
	src := t.TempDir()
	_ = os.MkdirAll(filepath.Join(src, ".apptown"), 0o755)
	_ = os.WriteFile(filepath.Join(src, ".apptown", "app.yaml"),
		[]byte("app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n"), 0o644)

	// A cached image is present, but the command crashes inside it.
	exitOne := exitStatusOne()
	if exitOne == nil {
		t.Fatal("exit fixture unexpectedly succeeded")
	}
	f.Daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "run" {
			return []byte("Traceback (most recent call last):\n  File \"/app/manage.py\", line 8\nModuleNotFoundError: No module named 'django'"), exitOne, true
		}
		return nil, nil, false
	})

	sc := []conformance.DeclaredCheck{{Block: "lang/django", Version: 5,
		Check: conformance.CheckDeclaration{Name: "django-check-deploy", Evaluator: "tool", Tool: "django-check-deploy", Severity: "high", Expects: "deploy checks pass"}}}
	results, findings, errs := f.Scanners.RunToolChecks(t.Context(), sc,
		ToolContext{Context: f.Context, ProjectID: f.ProjectID, SHA: "abcdef1234567890", BuildDir: src, AllowBuild: true}, "test")

	if len(findings) != 0 {
		t.Fatalf("a crash must NOT produce a conformance finding, got %+v", findings)
	}
	if errs != 1 || results[0].Verdict != workflow.VerdictError {
		t.Fatalf("want error verdict + 1 error, got verdict=%v errs=%d", results[0].Verdict, errs)
	}
}

// exitStatusOne is the error a process that exited with status 1 returns:
// what the Docker CLI reports when the command in the image fails.
func exitStatusOne() error {
	return (execution.Runner{}).Run(context.Background(), execution.Local, execution.Request{Argv: []string{"sh", "-c", "exit 1"}, Timeout: 10 * time.Second}).RunErr
}
