package workflowexec

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/progress"
	"github.com/flokkit/apptown/internal/strs"
	"github.com/flokkit/apptown/stock"
)

// The janitor's stable-direct steps are named scan steps over the stock
// listers. A direct pre-release the change adds fails the step; one already
// on the base commit (an owner's choice) is carried and passes; the report is
// kept as a prerelease-report, which the Security tab does not read. The
// lister is faked; what it lists is proved by the stock integration test.
//
// Kills: a lister that is not bound as the stock manifest declares it, a
// named manual scanner that loses the base comparison, and a report filed as
// a scan-report.
func TestADirectPreReleaseFailsOnlyWhenTheChangeAddsIt(t *testing.T) {
	sarif := func(packages ...string) string {
		var results []string
		for _, p := range packages {
			results = append(results, `{"ruleId":"direct-prerelease","level":"error","message":{"text":"`+p+` is a direct dependency in package.json and a pre-release"},"partialFingerprints":{"package":"package.json:`+p+`"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"package.json"}}}]}`)
		}
		return `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"npm","rules":[{"id":"direct-prerelease"}]}},"results":[` + strings.Join(results, ",") + `]}]}`
	}
	for _, c := range []struct {
		name            string
		base, candidate string
		want            workflow.StepState
	}{
		{"added by the change", sarif(), sarif("vite@9.0.0-beta.1"), workflow.StatusFail},
		{"already on main", sarif("vite@9.0.0-beta.1"), sarif("vite@9.0.0-beta.1"), workflow.StatusPass},
		{"moved to another pre-release", sarif("vite@9.0.0-beta.1"), sarif("vite@9.0.0-beta.2"), workflow.StatusFail},
	} {
		t.Run(c.name, func(t *testing.T) {
			spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "check", Steps: []schema.Step{{Name: "stable-direct-node", Uses: "scan", With: map[string]any{"scanner": "npm-direct-prereleases"}}}}}}
			f, _ := newNodeFixFixture(t, "stable-direct", spec)
			m := stockLister(t, "npm-direct-prereleases")
			m.AppliesTo = []string{"repository"} // the fixture's contract declares no node part
			cfg, err := strs.JSON(m)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := integration.NewWithClock(f.db, nil).Upsert(f.ctx, m.Name, "scanner", "tool", cfg, true); err != nil {
				t.Fatal(err)
			}
			report := func(body string) execution.Outcome {
				exit := 0
				if strings.Contains(body, "ruleId") {
					exit = 1
				}
				return execution.Outcome{Termination: execution.Exited, ExitCode: exit, Report: []byte(body)}
			}
			// The base commit is scanned through the evaluator, the candidate
			// through the scan step's executor.
			scanners := verification.NewScanners(verification.ScannerDependencies{DB: f.db, Evaluator: verification.NewEvaluator(listerEffects{report(c.base)}, nil, stock.Versions())})
			directory := identity.New(f.db, identity.Config{Projects: project.NewCatalogue(f.db)})
			conformanceService := conformance.New(f.db, access.Checked{Authority: directory, Projects: project.New(f.db, directory)}, nil, nil, nil)
			scan := verification.NewScanSteps(verification.ScanDependencies{DB: f.db, Scanners: scanners, Conformance: conformanceService, DataDir: f.dir, Progress: &progress.Feeds{}, Executor: verification.Executor{Run: func(context.Context, execution.Request) execution.Outcome {
				return report(c.candidate)
			}}})
			f.scan = scan.RunScan
			ch, err := f.work.Create(f.ctx, workflow.ChangeDraft{ProjectID: f.projectID, Title: "dependency update"})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.work.PrepareWorkspace(f.ctx, ch.ID); err != nil {
				t.Fatal(err)
			}
			f.work.Stop()
			run, err := f.work.StartRun(f.ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "stable-direct"})
			if !errors.Is(err, workflow.ErrDispatchStopped) {
				t.Fatal(err)
			}
			source, err := workflow.ReadSourceIn(t.Context(), f.db, workflow.RunRef{ID: run.ID, ProjectID: f.projectID})
			if err != nil {
				t.Fatal(err)
			}
			workspace, err := source.Workspace(f.dir)
			if err != nil {
				t.Fatal(err)
			}
			updateWrite(t, workspace, "candidate.txt", "updated")
			updateCommit(t, workspace, "candidate")
			if _, err := f.branches.Publish(t.Context(), scm.Publication{ProjectID: f.projectID, Repository: f.hub, Branch: source.Branch, From: workspace, Grant: scm.Grant{Holder: scm.RunHolder(run.ID)}}); err != nil {
				t.Fatal(err)
			}
			var step *workflow.Step
			for _, s := range queueSteps(t, f, run) {
				if s.Type == "scan" {
					step = s
				}
			}
			queueExecute(t, f, step)
			status, ev := stepStatus(t, f, step.ID)
			if status != c.want {
				t.Fatalf("got %s, want %s: %+v", status, c.want, ev)
			}
			artifacts, err := workflow.ReadRunArtifactsIn(t.Context(), f.db, workflow.RunRef{ID: run.ID, ProjectID: f.projectID}, workflow.ArtifactSelection{})
			if err != nil {
				t.Fatal(err)
			}
			kinds := []string{}
			for _, a := range artifacts {
				if a.Name == m.Name {
					kinds = append(kinds, a.Kind)
				}
			}
			if strings.Join(kinds, ",") != "prerelease-report" {
				t.Fatalf("the lister's report is kept as %v, want one prerelease-report", kinds)
			}
		})
	}
}

type listerEffects struct{ out execution.Outcome }

func (e listerEffects) Run(context.Context, execution.Request) execution.Outcome { return e.out }
func (listerEffects) Observe(context.Context, ids.ID, string, string) (conformance.Outcome, string) {
	return conformance.DoesNotConform, "not used"
}

func stockLister(t *testing.T, name string) check.Scanner {
	t.Helper()
	raw, err := fs.ReadFile(stock.FS, "scanners.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Scanners []check.Scanner `yaml:"scanners"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, m := range doc.Scanners {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("stock/scanners.yaml does not ship %s", name)
	return check.Scanner{}
}
