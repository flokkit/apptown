package deployment

import (
	"context"
	"testing"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/workflow"
)

func TestALandingRefusesUnclaimedWorkWithoutSideEffects(t *testing.T) {
	t.Parallel()
	s := New(context.Background(), nil, Dependencies{})
	for _, claim := range []ClaimedExecution{nil, (*workflow.Execution)(nil), &workflow.Execution{}} {
		if _, err := s.RunLanding(context.Background(), claim, LandingInput{}); err == nil {
			t.Fatal("landing accepted unclaimed work")
		}
	}
}

func TestDockerScopeRejectsMissingTargetBeforeUsingLocalDaemon(t *testing.T) {
	t.Parallel()
	calls := 0
	s := New(context.Background(), nil, assembled(nil, testRuntime{Scope: docker.Scope{Client: dockerClientFunc(func(context.Context, string, docker.Command) ([]byte, error) { calls++; return nil, nil })}}, Dependencies{}))
	for _, target := range []*Target{nil, {Kind: KindPortainer}, {Kind: KindDockerHost, Origin: OriginAttached, Name: "remote"}} {
		if _, err := s.hosts.DockerScope(context.Background(), target); err == nil {
			t.Fatal("invalid target accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid target reached a daemon")
	}
}
