package workflowexec

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/packagefacts"
	"github.com/flokkit/apptown/stock"
)

// majorFacts answers from fixed lines, or settledMajor for a package it
// does not name; every read is counted.
type majorFacts struct {
	lines map[string]packagefacts.Lines
	reads *atomic.Int32
}

// settledMajor: the newest major, 2, has settled at any date the fixtures use.
var settledMajor = packagefacts.Lines{Newest: "2", First: packagefacts.Release{Version: "2.0.0", Published: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}, Since: 3}

func (m majorFacts) Lines(_ context.Context, p packagefacts.Package, _ string, _ func(string) string) (packagefacts.Lines, error) {
	m.reads.Add(1)
	if l, ok := m.lines[p.Name]; ok {
		return l, nil
	}
	return settledMajor, nil
}
func (m majorFacts) Support(context.Context, packagefacts.Package) (packagefacts.Support, error) {
	m.reads.Add(1)
	return packagefacts.Support{}, nil
}

// npmLock is a package-lock.json with direct dependencies (name → version)
// and indirect ones.
func npmLock(direct, indirect map[string]string) string {
	root := map[string]string{}
	packages := map[string]any{}
	for name, v := range direct {
		root[name] = "*"
		packages["node_modules/"+name] = map[string]string{"version": v}
	}
	for name, v := range indirect {
		packages["node_modules/"+name] = map[string]string{"version": v}
	}
	packages[""] = map[string]any{"dependencies": root}
	raw, _ := json.Marshal(map[string]any{"lockfileVersion": 3, "packages": packages})
	return string(raw)
}

// At Including major (aggressive) the routine step refuses a direct
// dependency's major move that no rule allows before the model judges, and
// names the rule for one that is due. An indirect package's major change
// needs no decision. At Routine a direct dependency leaving its line fails
// before the judge, and a minor of a package endoflife.date does not track
// stays on its line.
func TestRoutineStepDecidesDirectMajorMoves(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	settled := packagefacts.Lines{Newest: "2", First: packagefacts.Release{Version: "2.0.0", Published: now.AddDate(-1, 0, 0)}, Since: 4}
	young := packagefacts.Lines{Newest: "2", First: packagefacts.Release{Version: "2.0.0", Published: now.AddDate(0, -1, 0)}, Since: 4}
	for _, c := range []struct {
		name, level string
		after       map[string]string
		pass, judge bool
		reads       bool
		reason      []string
	}{
		{name: "a due major passes and names its rule", level: "aggressive", after: map[string]string{"example": "2.0.1"}, pass: true, judge: true, reads: true,
			reason: []string{"Release line moves:", "example 1 → 2 due: 2.0.0 was released 2025-11-21; 4 releases since."}},
		{name: "a major that is not due fails before the judge", level: "aggressive", after: map[string]string{"example": "2.0.1", "other": "2.0.0"}, reads: true,
			reason: []string{"not due", "other 1 → 2 not due", "Keep other on 1"}},
		{name: "routine refuses leaving a line before the judge", level: "routine", after: map[string]string{"example": "2.0.1"}, reads: true,
			reason: []string{"example 1 → 2 not allowed at Routine: 2.0.1 leaves its line 1; Routine keeps it on 1."}},
		{name: "routine moves an untracked minor", level: "routine", after: map[string]string{"other": "1.5.0"}, pass: true, judge: true, reads: true},
		{name: "routine reads nothing for a patch", level: "routine", after: map[string]string{"other": "1.4.1"}, pass: true, judge: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			raw, _ := stock.FS.ReadFile("janitor-update.yaml")
			doc, err := schema.ParseWorkflow(raw)
			if err != nil {
				t.Fatal(err)
			}
			f, _ := newNodeFixFixture(t, "updates", doc.Spec)
			f.bound = c.level
			reads := new(atomic.Int32)
			f.commands.majors = verification.Majors{Facts: majorFacts{lines: map[string]packagefacts.Lines{"example": settled, "other": young}, reads: reads}, DB: f.db, Now: func() time.Time { return now }}
			updateWrite(t, f.hub, "package-lock.json", npmLock(map[string]string{"example": "1.0.0", "other": "1.4.0"}, map[string]string{"inner": "1.0.0"}))
			updateCommit(t, f.hub, "dependencies")
			installFakePi(f, func(_ int, req execution.Request) execution.Outcome {
				direct := map[string]string{"example": "1.0.0", "other": "1.4.0"}
				for k, v := range c.after {
					direct[k] = v
				}
				// The moved direct package's own range requires inner 3.
				updateWrite(t, req.Workspace, "package-lock.json", npmLock(direct, map[string]string{"inner": "3.0.0"}))
				updateCommit(t, req.Workspace, "update")
				return execution.Outcome{Termination: execution.Exited, Output: piSays("updated")}
			})
			judged := false
			f.commands.judge = updateJudgeFunc(func(_ context.Context, _ workflow.Source, item check.Item, _ string) (workflow.CheckReceipt, error) {
				judged = true
				if c.level == "aggressive" && !strings.Contains(item.Rubric, "Release line moves AppTown allowed:\n  example 1 → 2 due") {
					t.Errorf("the judge was not told the allowed move: %s", item.Rubric)
				}
				return workflow.CheckReceipt{Verdict: workflow.VerdictPass, Detail: "within the level"}, nil
			})
			run := queueRun(t, f, "updates", "updates")
			steps := queueSteps(t, f, run)
			queueExecute(t, f, steps["update"])
			queueExecute(t, f, steps["routine"])
			status, ev := stepStatus(t, f, steps["routine"].ID)
			encoded, _ := json.Marshal(ev)
			want := workflow.StatusFail
			if c.pass {
				want = workflow.StatusPass
			}
			if status != want || judged != c.judge || (reads.Load() > 0) != c.reads {
				t.Fatalf("%s judged=%t reads=%d: %s", status, judged, reads.Load(), encoded)
			}
			reason, _ := ev["reason"].(string)
			for _, w := range c.reason {
				if !strings.Contains(reason, w) {
					t.Errorf("reason lacks %q: %s", w, reason)
				}
			}
			if strings.Contains(string(encoded), `"name":"inner"`) {
				t.Errorf("an indirect package got a major decision: %s", encoded)
			}
		})
	}
}
