package apptown

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/httpapi"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adrs"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func TestEvidenceOwnersRefreshWorkWithoutTransportFollowUp(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"adr/require", "artifact/require"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			a, ctx, change := changeScenario(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{{Name: "evidence", Uses: kind}}}}})
			a.Work.Stop()
			run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "test"})
			if !errors.Is(err, workflow.ErrDispatchStopped) {
				t.Fatal(err)
			}
			if kind == "adr/require" {
				caller := verifiedContext(context.Background(), access.Principal{Kind: "user", Name: "alice", Via: "open"})
				record, err := a.ADRs.Create(caller, adrs.Create{ProjectID: change.ProjectID, ChangeID: change.ID, Title: "Chosen design"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := a.ADRs.Edit(caller, adrs.Edit{ID: record.ID, Decision: adrs.Accept}); err != nil {
					t.Fatal(err)
				}
			} else {
				req := httptest.NewRequest("POST", "/", strings.NewReader(`{"kind":"report","body":"evidence"}`))
				req = testRequestAs(req, "alice")
				req.SetPathValue("id", fmt.Sprint(run.ID))
				response := httptest.NewRecorder()
				httpapi.NewRuns(a.Work, nil).AddArtifact(response, req.WithContext(callerContext(req)))
				if response.Code != 201 {
					t.Fatalf("attachment: %d %s", response.Code, response.Body.String())
				}
				rebuildWork(t, a, func(deps *workflow.Dependencies) {
					original := deps.Authority
					deps.Authority = workPermissionFunc(func(ctx context.Context, p access.Permission) (access.Caller, error) {
						if p.Capability == access.CapEvidenceAttach {
							return access.Caller{}, &access.Denied{Reason: "attachment revoked"}
						}
						return original.Require(ctx, p)
					})
				})
				denied := httptest.NewRequest("POST", "/", strings.NewReader(`{"kind":"report"}`))
				denied = testRequestAs(denied, "alice")
				denied.SetPathValue("id", fmt.Sprint(run.ID))
				response = httptest.NewRecorder()
				httpapi.NewRuns(a.Work, nil).AddArtifact(response, denied.WithContext(callerContext(denied)))
				if response.Code != 403 {
					t.Fatalf("command denial became %d: %s", response.Code, response.Body.String())
				}
				if attached, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView); err != nil || len(attached.Artifacts) != 1 {
					t.Fatalf("a denied attachment was recorded: %+v %v", attached, err)
				}

			}
			snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
			if err != nil || len(snapshot.Steps) != 1 || snapshot.Steps[0].Status != workflow.StatusPass {
				t.Fatalf("evidence did not refresh: %+v %v", snapshot, err)
			}
			if err := a.Work.RefreshAssertions(a.asEngine(context.Background()), run.ID); err != nil {
				t.Fatal(err)
			}
			if n := runEventCount(t, a, run.ID, "check.evaluated"); n != 1 {
				t.Fatalf("check.evaluated receipts: %d", n)
			}
		})
	}
}
