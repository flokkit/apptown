package workflow

import (
	"context"
	"encoding/json"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fix"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/stock"
)

func stockNodeFix(t *testing.T) []fix.Declaration {
	t.Helper()
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
			return ds
		}
	}
	t.Fatal("node declaration missing")
	return nil
}
func TestOnlyFailedSourceScansWithFixableFindingsSupplyCallTargets(t *testing.T) {
	for _, tc := range []struct {
		name, ecosystem string
		status          StepState
		want            int
	}{
		{"OS package", "Debian", StatusFail, 0},
		{"source npm", "npm", StatusFail, 1},
		{"scanner error", "npm", StatusError, 0},
		{"passing scan", "npm", StatusPass, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []check.Finding
			var why string
			ds := stockNodeFix(t)
			rig := stepAuthorityRunnerFixture(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{
				{Name: "scan-source", Uses: "scan"},
				{Name: "fix", Uses: "workflow/call", With: map[string]any{"workflow": "child", "whenFailed": []string{"scan-source"}, "findings": "fixable"}},
			}}}}, map[string]Runner{
				"scan": func(task *Execution) {
					if _, err := task.Claim(); err != nil {
						t.Error(err)
						return
					}
					if err := task.InAttempt("scan", func(tx *database.Tx, attempt int64) error {
						return conformance.RecordScanObservationIn(task.Context(), tx, conformance.ScanObservation{ProjectID: task.Run().ProjectID, RunID: task.Run().ID, StepID: task.Step().ID, Attempt: attempt, Subject: conformance.ScanSubject{On: "source"}, Scanner: "osv", Verdict: tc.status.String(), ObservedAt: time.Now(), Findings: conformance.WrapFindings([]check.Finding{{Fingerprint: "finding", Package: &check.PackageRef{Name: "example", Ecosystem: tc.ecosystem, FixedIn: "1.0.1"}}})})
					}); err != nil {
						t.Error(err)
						return
					}
					if err := task.Finish(EncodeOutcome(tc.status, map[string]string{"reason": "scan answered"}, nil)); err != nil {
						t.Error(err)
					}
				},
				"workflow/call": func(task *Execution) {
					var err error
					got, why, err = task.fixableTargets(task.Context(), []string{"scan-source"}, ds)
					if err != nil {
						t.Error(err)
					}
					if _, err := task.Claim(); err != nil {
						t.Error(err)
						return
					}
					task.Finish(EncodeOutcome(StatusPass, map[string]any{"targets": got, "reason": why}, nil))
				},
			})
			rig.Work.Stop()
			ctx := stepVerifiedContext(context.Background(), access.Principal{Kind: "user", Name: "owner", Via: "token"})
			snapshot, err := rig.Work.Snapshot(ctx, rig.RunID, DetailView)
			if err != nil {
				t.Fatal(err)
			}
			var scan, call ids.ID
			for _, s := range snapshot.Steps {
				if s.Name == "scan-source" {
					scan = s.ID
				} else {
					call = s.ID
				}
			}
			if err := rig.Work.ExecuteStep(ctx, scan); err != nil {
				t.Fatal(err)
			}
			if err := rig.Work.ExecuteStep(ctx, call); err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.want {
				t.Fatalf("targets=%+v reason=%q", got, why)
			}
			snapshot, err = rig.Work.Snapshot(ctx, rig.RunID, DetailView)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range snapshot.Steps {
				if s.ID == scan && s.Status != tc.status {
					t.Fatalf("observing verdict changed: %s", s.Status)
				}
				if s.ID == call {
					var evidence struct {
						Targets []check.Finding `json:"targets"`
					}
					if err := json.Unmarshal(s.Evidence, &evidence); err != nil || len(evidence.Targets) != tc.want {
						t.Fatalf("recorded targets %s: %v", s.Evidence, err)
					}
				}
			}
		})
	}
}

func TestConditionalCallsRunAfterObservationsWithoutChangingTheirVerdicts(t *testing.T) {
	calls := 0
	finish := func(status StepState) Runner {
		return func(task *Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			if err := task.Finish(EncodeOutcome(status, map[string]string{"reason": "observed"}, nil)); err != nil {
				t.Error(err)
			}
		}
	}
	rig := stepAuthorityRunnerFixture(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{
		{Name: "observe", Steps: []schema.Step{
			{Name: "source", Uses: "scan"}, {Name: "running", Uses: "scan"},
			{Name: "fix", Uses: "workflow/call", With: map[string]any{"workflow": "child", "whenFailed": []string{"source"}, "findings": "fixable"}},
			{Name: "accept", Uses: "approval"},
		}, Gate: &schema.Gate{Require: []string{"fix", "accept"}}},
	}}, map[string]Runner{
		"script": finish(StatusPass),
		"scan": func(task *Execution) {
			if task.Step().Name == "source" {
				finish(StatusFail)(task)
			} else {
				finish(StatusError)(task)
			}
		},
		"workflow/call": func(task *Execution) { calls++; finish(StatusPass)(task) },
	})
	if err := rig.Work.DispatchStarted(rig.RunID); err != nil {
		t.Fatal(err)
	}
	rig.Work.Wait()
	ctx := stepVerifiedContext(t.Context(), access.Principal{Kind: "user", Name: "owner", Via: "token"})
	snapshot, err := rig.Work.Snapshot(ctx, rig.RunID, DetailView)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("call ran %d times: %+v", calls, snapshot.Steps)
	}
	for _, s := range snapshot.Steps {
		if s.Name == "source" && s.Status != StatusFail || s.Name == "running" && s.Status != StatusError || s.Name == "accept" && s.Status != StatusPending {
			t.Fatalf("observation or acceptance was reinterpreted: %+v", s)
		}
	}
}

func TestRunningTargetsAreImageOnlyPublicPartFindingsAboveThreshold(t *testing.T) {
	for _, tc := range []struct {
		name, part, severity string
		shared               bool
		want                 int
	}{
		{"public image", "web", "high", false, 1},
		{"another part", "worker", "high", false, 0},
		{"below threshold", "web", "medium", false, 0},
		{"source package", "web", "high", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []check.Finding
			finding := check.Finding{Fingerprint: "image-only", Source: "trivy", Severity: tc.severity, Identifiers: []string{"CVE-image"}, Package: &check.PackageRef{Name: "libc", Ecosystem: "Alpine"}}
			rig := stepAuthorityRunnerFixture(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "scan", Steps: []schema.Step{
				{Name: "source", Uses: "scan"}, {Name: "running", Uses: "scan", With: map[string]any{"on": "running", "failOn": "high"}},
				{Name: "fix", Uses: "workflow/call"},
			}}}}, map[string]Runner{
				"scan": func(task *Execution) {
					if _, err := task.Claim(); err != nil {
						t.Error(err)
						return
					}
					subject := conformance.ScanSubject{On: "source"}
					var findings []check.Finding
					status := StatusPass
					if task.Step().Name == "running" {
						subject = conformance.ScanSubject{On: "running", Part: tc.part}
						findings = []check.Finding{finding}
						status = StatusFail
					} else if tc.shared {
						findings = []check.Finding{finding}
					}
					if err := task.InAttempt("scan", func(tx *database.Tx, attempt int64) error {
						return conformance.RecordScanObservationIn(task.Context(), tx, conformance.ScanObservation{ProjectID: task.Run().ProjectID, RunID: task.Run().ID, StepID: task.Step().ID, Attempt: attempt, Subject: subject, Scanner: finding.Source, Verdict: status.String(), ObservedAt: time.Now(), Findings: conformance.WrapFindings(findings)})
					}); err != nil {
						t.Error(err)
						return
					}
					task.Settle(EncodeOutcome(status, map[string]any{"reason": "observed"}, nil))
				},
				"workflow/call": func(task *Execution) {
					var err error
					got, _, err = task.fixableTargets(task.Context(), []string{"source", "running"}, nil)
					if err != nil {
						t.Error(err)
					}
					if _, err := task.Claim(); err != nil {
						t.Error(err)
						return
					}
					task.Settle(EncodeOutcome(StatusPass, nil, nil))
				},
			})
			rig.Work.Stop()
			rig.Work.dataDir = t.TempDir()
			source, err := ReadSourceIn(t.Context(), rig.DB, RunRef{ID: rig.RunID, ProjectID: rig.ProjectID})
			if err != nil {
				t.Fatal(err)
			}
			repo, err := source.Repository(rig.Work.dataDir)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(repo, ".apptown"), 0700); err != nil {
				t.Fatal(err)
			}
			gittest.Run(t, repo, "init", "-q", "-b", "main")
			if err := os.WriteFile(filepath.Join(repo, ".apptown", "app.yaml"), []byte("app:\n  web:\n    build: .\n    public: true\n  worker:\n    build: worker\n"), 0600); err != nil {
				t.Fatal(err)
			}
			gittest.Run(t, repo, "add", ".")
			gittest.Run(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "contract")
			// This fixture normally represents a project without a repository. Set its
			// default through the project owner before the source is read by the call.
			if err := rig.DB.InTx(t.Context(), func(tx *database.Tx) error {
				if err := project.SetDefaultBranchIn(t.Context(), tx, rig.ProjectID, "main"); err != nil {
					return err
				}
				return takeChangeForRunIn(t.Context(), tx, RunRef{ID: rig.RunID, ProjectID: rig.ProjectID}, nil, time.Now())
			}); err != nil {
				t.Fatal(err)
			}
			ctx := stepVerifiedContext(t.Context(), access.Principal{Kind: "user", Name: "owner", Via: "token"})
			snapshot, err := rig.Work.Snapshot(ctx, rig.RunID, DetailView)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range snapshot.Steps {
				if err := rig.Work.ExecuteStep(ctx, s.ID); err != nil {
					t.Fatal(err)
				}
			}
			if len(got) != tc.want {
				t.Fatalf("targets: %+v want %d", got, tc.want)
			}
			if len(got) > 0 && (got[0].ScanPoint != "image" || got[0].Source != "trivy") {
				t.Fatalf("target lost scan point/binding: %+v", got)
			}
		})
	}
}
