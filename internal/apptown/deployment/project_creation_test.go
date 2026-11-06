package deployment

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
)

type creationDecision struct{ err error }

func (d creationDecision) RequireIn(_ context.Context, _ *database.Tx, pr access.Principal, _ access.Permission) (access.Principal, error) {
	return pr, d.err
}

func TestNewProjectDefaultRequiresCurrentDeployAuthority(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"confirmed", "denied", "cannot read authority"} {
		t.Run(scenario, func(t *testing.T) {
			s, world, in, _ := configurationFixture(t)
			s.hosts.settings.BuiltInTarget = "local"
			if _, err := s.Targets.Put(t.Context(), &Target{Name: "local", Kind: KindDockerHost, Origin: OriginBuiltIn, Status: TargetReady}); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("authority unavailable")
			decision := error(&access.Denied{Reason: "deploy grant withdrawn"})
			if scenario == "cannot read authority" {
				decision = failure
			}
			if scenario == "confirmed" {
				decision = nil
			}
			s.creationAuthority = creationDecision{decision}
			caller, err := world.Require(t.Context(), access.Permission{})
			if err != nil {
				t.Fatal(err)
			}
			ctx := access.WithCaller(t.Context(), caller)
			err = s.db.InTx(ctx, func(tx *database.Tx) error { return s.InitializeProjectIn(ctx, tx, in.ProjectID) })
			if scenario == "denied" && err != nil {
				t.Fatal(err)
			}
			if scenario == "cannot read authority" && !errors.Is(err, failure) {
				t.Fatalf("lost authority failure: %v", err)
			}
			deps, err := s.Deployments.ForProject(t.Context(), in.ProjectID)
			if scenario == "confirmed" {
				if err != nil || len(deps) != 1 || !deps[0].FeedConfirmed {
					t.Fatalf("confirmed default: %+v %v", deps, err)
				}
				saved, err := releasePrincipal(deps[0].feedAuthority)
				if err != nil || saved.ID != caller.Principal.ID || saved.Credential != caller.Principal.Credential {
					t.Fatalf("creating credential changed: %+v %v", saved, err)
				}
				return
			}
			if err != nil || len(deps) != 0 {
				t.Fatalf("deployment without deploy authority: %+v %v", deps, err)
			}
		})
	}
}
