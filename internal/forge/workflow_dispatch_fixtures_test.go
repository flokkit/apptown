package forge

import (
	"testing"

	"flokkit.io/forge/internal/forge/workflow"
)

// Tests bind fake runners before first dispatch: Work is rebuilt with
// them as its Runners dependency; everything else is the production
// wiring, including the advance after a runner finishes.
type dispatchFixture struct {
	app     *App
	runners map[string]workflow.Runner
}

func installDispatchFixture(t *testing.T, a *App) *dispatchFixture {
	t.Helper()
	fixture := &dispatchFixture{app: a, runners: map[string]workflow.Runner{}}
	bound := map[string]workflow.Runner{}
	for typ, run := range a.workDependencies().Runners {
		bound[typ] = func(task *workflow.Execution) {
			if fake := fixture.runners[typ]; fake != nil {
				fake(task)
			} else {
				run(task)
			}
		}
	}
	rebuildWork(t, a, func(deps *workflow.Dependencies) { deps.Runners = bound })
	return fixture
}
func (d *dispatchFixture) wait()    { d.app.Work.Wait() }
func (d *dispatchFixture) recover() { d.app.Work.ResumePending() }
