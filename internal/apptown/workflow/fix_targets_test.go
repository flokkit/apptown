package workflow

import (
	"context"
	"encoding/json"
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
