package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/ids"
)

type landingHistoryFixture struct {
	attempts []LandingDeployAttempt
	err      error
	calls    int
}

func (f *landingHistoryFixture) LandingDeployAttempts(context.Context, ids.ID) ([]LandingDeployAttempt, error) {
	f.calls++
	return f.attempts, f.err
}

func TestDeployHistoryShowsAnInvocationBeforePlacementAndChecksAuthority(t *testing.T) {
	t.Parallel()
	s, authority, _, project := listingFixture(t, 0, absentObservation)
	reader := &landingHistoryFixture{attempts: []LandingDeployAttempt{
		{Attempt: 1, Step: LandingDeployStep{Config: json.RawMessage(`{}`), Status: "running"}},
		{Attempt: 2, Step: LandingDeployStep{Config: json.RawMessage(`{"deployment":"staff"}`), Status: "fail"}},
	}}
	s.landings = reader
	got, err := s.DeployHistory(t.Context(), project, "main")
	if err != nil || len(got) != 1 || got[0].Attempt != 1 {
		t.Fatalf("unplaced main: %+v %v", got, err)
	}
	got, err = s.DeployHistory(t.Context(), project, "staff")
	if err != nil || len(got) != 1 || got[0].Attempt != 2 {
		t.Fatalf("staff: %+v %v", got, err)
	}
	if _, err := s.DeployHistory(t.Context(), project, "missing"); err == nil {
		t.Fatal("invented deployment")
	}
	reader.err = errors.New("history unavailable")
	if _, err := s.DeployHistory(t.Context(), project, "main"); !errors.Is(err, reader.err) {
		t.Fatalf("lost source failure: %v", err)
	}
	before := reader.calls
	authority.err = errors.New("denied")
	if _, err := s.DeployHistory(t.Context(), project, "main"); !errors.Is(err, authority.err) || reader.calls != before {
		t.Fatalf("read before authority: %v", err)
	}
}
