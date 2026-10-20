package workflow

import (
	"context"
	"errors"
	"testing"

	"flokkit.io/forge/internal/access"
)

func TestWorkInstrumentReadsStayInsideProjectAndOAuthScope(t *testing.T) {
	t.Parallel()
	a := stepAuthorityFixture(t, ApprovalConfig{})
	for _, pr := range []access.Principal{
		{Kind: "instrument", Name: "run/1", Instrument: "run", Via: "run", ProjectID: a.OtherProjectID},
		{Kind: "instrument", Name: "owner via console/c", Person: "owner", Instrument: "console", Via: "console", ProjectID: a.OtherProjectID},
		{Kind: "user", Name: "owner", Via: "oauth", Scopes: nil},
	} {
		if got, err := a.Work.Snapshot(stepVerifiedContext(context.Background(), pr), a.RunID, DetailView); got != nil || err == nil {
			t.Fatalf("outside read %+v: %+v %v", pr, got, err)
		}
	}
	pr := access.Principal{Kind: "instrument", Name: "run/1", Instrument: "run", Via: "run", ProjectID: a.ProjectID}
	out, err := a.Work.Snapshot(stepVerifiedContext(context.Background(), pr), a.RunID, DetailView)
	if err != nil || out.Run.ID != a.RunID {
		t.Fatalf("admitted run read: %+v %v", out, err)
	}
}

func TestWorkRunAuthorityWithoutTransportAndTypedBilling(t *testing.T) {
	t.Parallel()
	a := stepAuthorityFixture(t, ApprovalConfig{})
	for _, pr := range []access.Principal{
		{Kind: "user", Name: "outsider", Via: "token"},
		{Kind: "user", Name: "owner", Via: "oauth", Scopes: []string{"apptown:user:read"}},
		{Kind: "instrument", Name: "owner via console/c", Person: "owner", Instrument: "console", Via: "console", ProjectID: a.OtherProjectID},
	} {
		for _, call := range []func() (*Run, error){
			func() (*Run, error) {
				return a.Work.StartRun(stepVerifiedContext(context.Background(), pr), StartChangeRun{ChangeID: a.ChangeID, Workflow: "test"})
			},
			func() (*Run, error) {
				return a.Work.RequestLanding(stepVerifiedContext(context.Background(), pr), a.ChangeID)
			},
		} {
			run, err := call()
			var denied *access.Denied
			if run != nil || !errors.As(err, &denied) {
				t.Fatalf("unscoped invocation %+v: %+v %v", pr, run, err)
			}
		}
	}
}

func TestWorkAbandonmentEnforcesAuthorityWithoutRoutes(t *testing.T) {
	t.Parallel()
	a := stepAuthorityFixture(t, ApprovalConfig{})
	for _, pr := range []access.Principal{
		{Kind: "user", Name: "outsider", Via: "token"},
		{Kind: "user", Name: "owner", Via: "oauth", Scopes: []string{"apptown:user:read"}},
		{Kind: "instrument", Name: "owner via console/c", Person: "owner", Instrument: "console", Via: "console", ProjectID: a.OtherProjectID},
	} {
		for _, in := range []StopWork{{RunID: a.RunID}, {ChangeID: a.ChangeID}} {
			out, err := a.Work.Abandon(stepVerifiedContext(context.Background(), pr), in)
			var denied *access.Denied
			if out != nil || !errors.As(err, &denied) {
				t.Fatalf("unscoped stop %+v: %+v %v", pr, out, err)
			}
		}
	}
	wantRunState(t, a.DB, a.RunID, RunRunning)
}
