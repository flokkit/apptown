package deployment

import (
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"
)

func TestReviewRestartDoesNotRedeployAnAlreadyActiveDORelease(t *testing.T) {
	s, provider, db, pid, dep, bundle := doReleaseWorld(t)
	accepted := doAcceptRelease(t, s, pid, dep, bundle)
	dbtest.FailNextStatement(t, db, "UPDATE deployment_attempts SET observed_json")
	if err := s.ExecuteRelease(t.Context(), accepted.ID); err == nil {
		t.Fatal("expected checkpoint failure")
	}
	before := provider.Snapshot().DeploymentGeneration
	if before != 1 {
		t.Fatalf("provider did not deploy the first request: %d", before)
	}
	at, _, err := s.Outcomes.AttemptByKey(t.Context(), dep.ID, fmt.Sprintf("release-%d", accepted.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareRecovery(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := s.Recoverer().Run(t.Context(), at.ID)
	if err != nil || result.Outcome != "recovered" {
		t.Fatalf("recovery: %+v %v", result, err)
	}
	if after := provider.Snapshot().DeploymentGeneration; after != before {
		t.Fatalf("recovery deployed again although the provider already had the new version active: before=%d after=%d", before, after)
	}
}
