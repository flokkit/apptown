package workflowexec

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/progress"
	"github.com/flokkit/apptown/stock"
)

func TestTargetScanMustWitnessTheFindingGone(t *testing.T) {
	for _, mode := range []string{"remaining", "not applicable", "detached", "fixed", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "check", Steps: []schema.Step{{Name: "source", Uses: "scan", With: map[string]any{"on": "source", "scanner": "osv", "failOn": "critical"}}}}}}
			f, _ := newNodeFixFixture(t, "target", spec)
			applies := []string{"repository"}
			if mode == "not applicable" {
				applies = []string{"tool:ruby"}
			}
			cfg, _ := json.Marshal(map[string]any{"image": "scanner:test", "command": []string{"scan"}, "target": "source", "format": "osv", "report": ".apptown/osv.json", "kind": "vuln", "appliesTo": applies})
			if _, err := integration.NewWithClock(f.db, nil).Upsert(f.ctx, "osv", "scanner", "tool", string(cfg), true); err != nil {
				t.Fatal(err)
			}
			scanners := verification.NewScanners(verification.ScannerDependencies{DB: f.db, Evaluator: verification.NewEvaluator(targetScanEffects{}, nil, stock.Versions())})
			directory := identity.New(f.db, identity.Config{Projects: project.NewCatalogue(f.db)})
			conformanceService := conformance.New(f.db, access.Checked{Authority: directory, Projects: project.New(f.db, directory)}, nil, nil, nil)
			scan := verification.NewScanSteps(verification.ScanDependencies{DB: f.db, Scanners: scanners, Conformance: conformanceService, DataDir: f.dir, Progress: &progress.Feeds{}, Executor: verification.Executor{Run: func(context.Context, execution.Request) execution.Outcome {
				report := `{"results":[]}`
				if mode == "remaining" {
					report = `{"results":[{"packages":[{"package":{"name":"example","version":"1.0.0","ecosystem":"npm"},"vulnerabilities":[{"id":"CVE-example","database_specific":{"severity":"LOW"}}]}]}]}`
				}
				if mode == "malformed" {
					report = `no report`
				}
				return execution.Outcome{Termination: execution.Exited, Report: []byte(report)}
			}}})
			f.scan = scan.RunScan
			ch, err := f.work.Create(f.ctx, workflow.ChangeDraft{ProjectID: f.projectID, Title: "targeted fix"})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.work.PrepareWorkspace(f.ctx, ch.ID); err != nil {
				t.Fatal(err)
			}
			source := "osv"
			if mode == "detached" {
				source = "removed-scanner"
			}
			target, _ := json.Marshal([]check.Finding{{Source: source, Identifiers: []string{"CVE-example"}, Package: &check.PackageRef{Name: "example", Ecosystem: "npm", Version: "1.0.0"}}})
			inputs, _ := json.Marshal(map[string]string{"targets": string(target)})
			f.work.Stop()
			run, err := f.work.StartRun(f.ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "target", Inputs: inputs})
			if !errors.Is(err, workflow.ErrDispatchStopped) {
				t.Fatal(err)
			}
			steps := queueSteps(t, f, run)
			var step *workflow.Step
			for _, s := range steps {
				if s.Type == "scan" {
					step = s
				}
			}
			queueExecute(t, f, step)
			status, ev := stepStatus(t, f, step.ID)
			want := workflow.StatusError
			if mode == "remaining" {
				want = workflow.StatusFail
			}
			if mode == "fixed" {
				want = workflow.StatusPass
			}
			if status != want {
				t.Fatalf("%s: got %s want %s: %+v", mode, status, want, ev)
			}
		})
	}
}

// The base holds the same target. Routine severity and baseline subtraction
// would pass it, so neither may witness that a targeted fix succeeded.
type targetScanEffects struct{}

func (targetScanEffects) Run(context.Context, execution.Request) execution.Outcome {
	return execution.Outcome{Termination: execution.Exited, Report: []byte(`{"results":[{"packages":[{"package":{"name":"example","version":"1.0.0","ecosystem":"npm"},"vulnerabilities":[{"id":"CVE-example"}]}]}]}`)}
}
func (targetScanEffects) Observe(context.Context, ids.ID, string, string) (conformance.Outcome, string) {
	return conformance.DoesNotConform, "base target"
}
