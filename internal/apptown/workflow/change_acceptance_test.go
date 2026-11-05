package workflow

import (
	"github.com/flokkit/apptown/internal/access"
	"slices"
	"testing"
)

func TestSourceAcceptanceNamesTheApproverInsteadOfTheRequester(t *testing.T) {
	t.Parallel()
	a := stepAuthorityFixture(t, ApprovalConfig{})
	boss := stepVerifiedContext(t.Context(), access.Principal{Kind: "user", Name: "boss", Via: "session"})
	ch, err := a.Work.Change(boss, a.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := a.Work.AcceptanceByNumber(boss, a.ProjectID, "game", ch.Number)
	if err != nil || len(before.AcceptedBy) != 0 {
		t.Fatalf("unaccepted: %+v %v", before, err)
	}
	if _, err := a.Work.Approve(boss, Approval{StepID: a.StepID, Decision: VoteApprove}); err != nil {
		t.Fatal(err)
	}
	got, err := a.Work.AcceptanceByNumber(boss, a.ProjectID, "game", ch.Number)
	if err != nil || got.ChangeID != a.ChangeID || !slices.Equal(got.AcceptedBy, []string{"boss"}) {
		t.Fatalf("acceptance: %+v %v", got, err)
	}
	if _, err := a.Work.AcceptanceByNumber(boss, a.OtherProjectID, "other", ch.Number); err == nil {
		t.Fatal("source crossed project boundary")
	}
}
