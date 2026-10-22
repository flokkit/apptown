package deployment

import (
	"context"
	"strings"
	"testing"
)

func TestDeployedDeploymentIsNotSilentlyRebound(t *testing.T) {
	t.Parallel()
	s, _, in, target := configurationFixture(t)
	dep := configureForTest(t, s, in)
	at, _, err := s.Outcomes.Admit(context.Background(), Start{DeploymentID: dep.ID, TargetID: target.ID, Key: "first",
		Inputs: Inputs{Revision: "selected-revision", ImageDigest: "selected-image"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Outcomes.Complete(context.Background(), deploymentCompletionFor(at, "observed")); err != nil {
		t.Fatal(err)
	}
	other, err := s.Targets.Put(context.Background(), &Target{Name: "other", Kind: KindDockerHost, Origin: OriginBuiltIn, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	attrs := Attributes{Feed: "promotion", Audience: "team", DataClass: "non-production"}
	if _, err := s.Deployments.EnsureStanding(t.Context(), in.ProjectID, in.Name, other.ID, attrs); err == nil || !strings.Contains(err.Error(), "migration") {
		t.Fatalf("moving a deployed environment is a migration: %v", err)
	}
	attrs.Audience = "public"
	if _, err := s.Deployments.EnsureStanding(t.Context(), in.ProjectID, in.Name, target.ID, attrs); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("raising protection on a deployed environment goes through the gates: %v", err)
	}
	attrs.Feed, attrs.Audience = "manual", "team"
	if _, err := s.Deployments.EnsureStanding(t.Context(), in.ProjectID, in.Name, target.ID, attrs); err != nil {
		t.Fatalf("an ordinary setting still changes: %v", err)
	}
}
