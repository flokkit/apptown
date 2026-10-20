package verification

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/execfile"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids/idstest"
)

const buildContract = "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n"

// imageCheck runs apptown check on a change whose head has a buildable
// contract, with the image scanners named in scanners attached (name →
// format, "" for the exit-code contract). Images come from the fake
// daemon; every scanner process is a script that writes one high SARIF
// finding and exits with exit, so the base, when it has an image, holds
// the same finding. seedContract gives the starting revision an image.
func imageCheck(t *testing.T, scanners map[string]string, exit int, seedContract bool) map[string]any {
	t.Helper()
	f := newScannerOwnerFixture(t)
	f.Scanners.deployments = deployment.NewDeployments(f.DB)
	for name, format := range scanners {
		cfg := map[string]any{"image": "aquasec/trivy:test", "command": []string{"image", "--input", "${IMAGE_TAR}"}, "network": false, "kind": "vuln", "target": "image"}
		if format != "" {
			cfg["format"], cfg["report"] = format, ".apptown/semgrep.sarif"
		}
		f.bind(t, name, cfg)
	}
	binary := scannerProcess(t, exit)
	// The real runner, so each image is saved into the tree as in production.
	f.Scanners.exec.Run = LocalExecutor(execution.Runner{DockerBinary: binary}, f.Scanners.docker, "", nil).Run

	dataDir := t.TempDir()
	hub := filepath.Join(dataDir, "workspaces", "project-p")
	git := func(args ...string) string {
		t.Helper()
		out, err := gittest.Command(append([]string{"-C", hub, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.MkdirAll(filepath.Join(hub, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	contract := filepath.Join(hub, ".apptown", "app.yaml")
	git("init", "-q", "-b", "main")
	if seedContract {
		if err := os.WriteFile(contract, []byte(buildContract), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", "-f", ".apptown/app.yaml")
	}
	git("commit", "-q", "--allow-empty", "-m", "seed")
	seed := git("rev-parse", "HEAD")
	branch := "change-00000000-0007-7000-8000-000000000000"
	git("checkout", "-q", "-b", branch)
	if err := os.WriteFile(contract, []byte(buildContract+"# the app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-f", ".apptown/app.yaml")
	git("commit", "-q", "-m", "the app")
	head := git("rev-parse", "HEAD")

	changes := NewChanges(ChangeDependencies{DB: f.DB, DataDir: dataDir, Conformance: f.Conformance, Scanners: f.Scanners, Evaluator: NewEvaluator(f.Effects, nil)})
	ch := &workflow.Change{ID: idstest.N(7), ProjectID: f.ProjectID, Branch: branch, BaseSHA: seed}
	out, err := changes.Check(f.Context, ch, Selection{Commit: head})
	if err != nil {
		t.Fatal(err)
	}
	if out["feedbackOnly"] != true || out["checkErrors"].(int) != 0 {
		t.Fatalf("apptown check with image scanners: %+v", out)
	}
	return out
}

// scannerProcess is a docker binary for the real runner: it saves images
// as empty archives and, run as a scanner, writes one high SARIF finding
// into the mounted tree and exits with exit. The process boundary is the
// guarantee its tests need: the runner saving each image into the tree
// (O_EXCL) between scans.
func scannerProcess(t *testing.T, exit int) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "scanner")
	script := "#!/bin/sh\nif [ \"$1\" = save ]; then exit 0; fi\ncase \"$1\" in inspect) echo false; exit 0;; rm) exit 0;; esac\nfor arg do\n case \"$arg\" in *:/work) dir=${arg%:/work};; esac\ndone\nmkdir -p \"$dir/.apptown\"\ncat > \"$dir/.apptown/semgrep.sarif\" <<'REPORT'\n" + scannerSARIF(1) + "\nREPORT\nexit " + strconv.Itoa(exit) + "\n"
	if err := execfile.Write(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary
}

// imageReceipts are the answer's receipts of the named scanners.
func imageReceipts(t *testing.T, out map[string]any, names ...string) []workflow.CheckReceipt {
	t.Helper()
	var got []workflow.CheckReceipt
	for _, r := range out["checks"].(Results) {
		if slices.Contains(names, r.Check) {
			got = append(got, r)
		}
	}
	if len(got) != len(names) {
		t.Fatalf("image scanners run: %+v, want %v", out["checks"], names)
	}
	return got
}

// apptown check runs the landing's image scan point as feedback (soak
// finding 8): every attached image scanner looks at the image of the
// checked commit, and a high finding this change introduced fails the
// answer, which still says it is feedback only. On a first release (a
// seed with no contract, main never released) the finding is new and the
// receipt says why. Two scanners share one tree, as the stock trivy and
// trivy-license do.
//
// Kills: Changes.Check not calling RunImageScans; RunImageScans skipping
// image scanners; the head's saved image left for the next scanner
// (R5B-C02: the second scanner errors).
func TestForgeCheckRunsTheImageScanPoint(t *testing.T) {
	t.Parallel()
	out := imageCheck(t, map[string]string{"trivy": "sarif", "trivy-license": "sarif"}, 1, false)
	if out["ok"] != false || out["failing"].(int) != 2 {
		t.Fatalf("apptown check with image scanners: %+v", out)
	}
	for _, r := range imageReceipts(t, out, "trivy", "trivy-license") {
		if r.Verdict != workflow.VerdictFail || !strings.Contains(r.Output, "high: semgrep: django.security.audit.sql-injection") || r.BaseNote != firstReleaseNote {
			t.Fatalf("image scan receipt: %+v", r)
		}
	}
}

// When the starting revision has an image, each image scanner compares
// with it, and a finding already there is carried, not this change's: the
// answer is ok. The second scanner keeps its comparison too.
//
// Kills: the head's saved image left for the next scanner (R5B-C02: the
// second scanner's base cannot be saved, so its finding reads as new).
func TestForgeCheckCarriesImageFindingsAlreadyOnTheBase(t *testing.T) {
	t.Parallel()
	out := imageCheck(t, map[string]string{"trivy": "sarif", "trivy-license": "sarif"}, 1, true)
	if out["ok"] != true || out["failing"].(int) != 0 {
		t.Fatalf("apptown check with a carried image finding: %+v", out)
	}
	for _, r := range imageReceipts(t, out, "trivy", "trivy-license") {
		if r.Verdict != workflow.VerdictPass || r.Carried != 1 || !strings.HasPrefix(r.BaseNote, "compared with starting revision's image") {
			t.Fatalf("image scan receipt: %+v", r)
		}
	}
}

// An image scanner with no structured report keeps the exit-code
// contract, as the gate does: exit 1 is findings and fails the answer,
// exit 0 passes (R5B-C01).
//
// Kills: the exit-code fallback removed (exit 1 reads as a pass).
func TestForgeCheckKeepsAnImageScannersExitCode(t *testing.T) {
	t.Parallel()
	for exit, ok := range map[int]bool{1: false, 0: true} {
		out := imageCheck(t, map[string]string{"plain": ""}, exit, false)
		r := imageReceipts(t, out, "plain")[0]
		want := workflow.VerdictPass
		if !ok {
			want = workflow.VerdictFail
		}
		if out["ok"] != ok || (out["failing"].(int) == 0) != ok || r.Verdict != want {
			t.Fatalf("exit %d: %+v, receipt %+v", exit, out, r)
		}
	}
}

// A failing scan's reason names the findings, and on a first release says
// so instead of quoting why the starting revision gave no image.
//
// Kills: the reason dropping the findings list.
func TestAFailingScanReasonListsItsFindings(t *testing.T) {
	t.Parallel()
	failing := []conformance.Finding{{Finding: check.Finding{Severity: "high", Title: "stdlib: crypto/x509", Identifiers: []string{"CVE-2026-78667"}}}}
	reason := scanFailReason(failing, scanBase{Note: firstReleaseNote, Detail: "preparing starting revision's image failed: npm error"})
	for _, want := range []string{"first release", "high: stdlib: crypto/x509 (CVE-2026-78667)"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason %q does not say %q", reason, want)
		}
	}
	if strings.Contains(reason, "npm error") {
		t.Fatalf("reason quotes the starting revision's build: %q", reason)
	}
}
