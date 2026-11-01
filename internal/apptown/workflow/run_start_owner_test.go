package workflow

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
)

// A run started from a chat is owned by the chat's opener as the chat
// recorded them (their principal, not their name), and through the chat,
// so its runtime credential is judged on their account. A chat that recorded no opener owns its run
// unresolved, never by the name.
func TestAChatStartedRunIsOwnedByTheChatsOpener(t *testing.T) {
	t.Parallel()
	a := stepAuthorityFixture(t, ApprovalConfig{})
	ctx := context.Background()
	owner, err := identity.ActorIn(ctx, a.DB, "owner")
	if err != nil {
		t.Fatal(err)
	}
	spec := schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}
	admissions := newRunAdmissions(a.DB, NewStages(nil, func(string) bool { return false }), nil)
	for _, tc := range []struct {
		opener access.Principal
		want   access.Actor
	}{
		{access.Principal{ID: owner.Principal, Kind: "instrument", Name: "owner via console/c1", Person: "owner", Instrument: "console", ConsoleID: "c1", ProjectID: a.ProjectID},
			access.Actor{Principal: owner.Principal, Name: "owner", Via: access.ViaChat + ":c1"}},
		{access.Principal{Kind: "instrument", Name: "owner via console/c2", Person: "owner", Instrument: "console", ConsoleID: "c2", ProjectID: a.ProjectID},
			access.Actor{Name: "owner", Via: access.ViaChat + ":c2"}},
	} {
		actor := callerActor(access.Caller{Principal: tc.opener, Verified: true})
		run, err := admissions.records.create(ctx, StartRequest{ProjectID: a.ProjectID, WorkflowName: "test", InputsJSON: "{}", requestedBy: actor.InferenceOwner, owner: actor.Owner, receiptActor: actor.Receipt}, PreparedRun{Workflow: DefinitionRef{Name: "test", Version: 1}, Spec: spec})
		if err != nil {
			t.Fatal(err)
		}
		got, err := workflowsql.New(a.DB.SQL()).RunInferenceOwnerByID(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if (access.Actor{Principal: got.ActorPrincipal, Name: got.Actor, Via: got.ActorVia}) != tc.want {
			t.Errorf("%s: the run is owned by %+v, want %+v", tc.opener.Name, got, tc.want)
		}
	}
}
