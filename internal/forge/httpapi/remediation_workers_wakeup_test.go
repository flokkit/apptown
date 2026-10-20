package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/remediation"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

// This port is never called: no pass runs in the test, which examines only
// the request for one. It does not create Forge state.
type inactiveRemediationWork struct{}

func (inactiveRemediationWork) PrepareStop(context.Context, ids.ID) (*workflow.StopScope, error) {
	panic("unexpected Work call")
}
func (inactiveRemediationWork) AdmitRunIn(context.Context, *database.Tx, workflow.StartRequest, workflow.PreparedRun) (*workflow.Run, error) {
	panic("unexpected Work call")
}
func (inactiveRemediationWork) RecheckLandingIn(context.Context, *database.Tx, workflow.LandingRecheck) (workflow.LandingRechecked, error) {
	panic("unexpected Work call")
}
func (inactiveRemediationWork) DispatchStarted(ids.ID) error  { panic("unexpected Work call") }
func (inactiveRemediationWork) DispatchCommittedRetry(ids.ID) { panic("unexpected Work call") }

// A person's change to the pool asks for a pass at once, through the
// service's request port (the job queue in AppTown), not a loop of the
// handler's own. Kills: the handler not asking for a pass.
func TestRemediationAPIAsksForAPass(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	var asked atomic.Int32
	fixes := remediation.New(db, remediation.Dependencies{Work: inactiveRemediationWork{}, RequestPass: func(context.Context) error {
		asked.Add(1)
		return nil
	}})
	for want := int32(1); want <= 2; want++ {
		response := httptest.NewRecorder()
		NewRemediations(fixes).Workers(response, httptest.NewRequest(http.MethodPut, "/api/v1/workers", strings.NewReader(`{"kind":"remediation","count":0}`)))
		if response.Code != http.StatusOK {
			t.Fatalf("workers API: %d %s", response.Code, response.Body.String())
		}
		if asked.Load() != want {
			t.Fatalf("passes asked for after %d changes: %d", want, asked.Load())
		}
	}
}
