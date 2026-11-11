package deployment

import (
	"context"
	"errors"
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
	attrs := Attributes{Feed: "manual", Audience: "team", DataClass: "non-production"}
	if _, err := s.Deployments.EnsureStanding(t.Context(), in.ProjectID, in.Name, other.ID, attrs); !errors.Is(err, errDeploymentStaysOnTarget) {
		t.Fatalf("moving a deployed deployment needs a new one: %v", err)
	}
	attrs.Audience = "public"
	if _, err := s.Deployments.EnsureStanding(t.Context(), in.ProjectID, in.Name, target.ID, attrs); !errors.Is(err, errClassificationElsewhere) {
		t.Fatalf("classification changes only through its settings: %v", err)
	}
	attrs.Feed, attrs.Audience = "manual", "team"
	if _, err := s.Deployments.EnsureStanding(t.Context(), in.ProjectID, in.Name, target.ID, attrs); err != nil {
		t.Fatalf("an ordinary setting still changes: %v", err)
	}
}
