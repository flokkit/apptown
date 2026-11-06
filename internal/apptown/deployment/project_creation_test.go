package deployment

import (
	"context"
	"errors"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
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
			err = s.db.InTx(ctx, func(tx *database.Tx) error { return s.InitializeProjectIn(ctx, tx, in.ProjectID, CreationPlacement{}) })
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

// The owner must apply each creation choice, and reject an unoffered target
// even when a caller bypasses the wizard's list.
func TestNewProjectTargetChoiceIsEnforcedByOwner(t *testing.T) {
	t.Parallel()
	for _, choice := range []string{"", "cloud", "none", "private", "missing", "bucket", "static bucket"} {
		t.Run(choice, func(t *testing.T) {
			s, world, in, cloud := configurationFixture(t)
			s.creationAuthority = creationDecision{}
			s.hosts.settings.BuiltInTarget = "local"
			local, err := s.Targets.Put(t.Context(), &Target{Name: "local", Kind: KindDockerHost, Origin: OriginBuiltIn, Status: TargetReady})
			if err != nil {
				t.Fatal(err)
			}
			for _, target := range []*Target{
				{Name: "private", Kind: KindDockerHost, Origin: OriginAttached, Status: TargetReady},
				{Name: "bucket", Kind: KindS3Bucket, Origin: OriginAttached, Status: TargetReady},
			} {
				saved, err := s.Targets.Put(t.Context(), target)
				if err != nil {
					t.Fatal(err)
				}
				if target.Name == "bucket" {
					if err := s.Targets.Offer(t.Context(), saved, "*", OfferCaps{}, "fixture"); err != nil {
						t.Fatal(err)
					}
				}
			}
			caller, err := world.Require(t.Context(), access.Permission{})
			if err != nil {
				t.Fatal(err)
			}
			ctx := access.WithCaller(t.Context(), caller)
			contracts := []*appcontract.Contract{world.contract}
			selection := choice
			if choice == "static bucket" {
				selection = "bucket"
				c, err := appcontract.Parse([]byte("version: 2\napp:\n  web:\n    public: true\n    static: dist\n"))
				if err != nil {
					t.Fatal(err)
				}
				contracts = []*appcontract.Contract{c}
			}
			options, err := s.CreationTargets(ctx, "", contracts)
			if err != nil {
				t.Fatal(err)
			}
			if len(options) != 3 {
				t.Fatalf("offered targets: %+v", options)
			}
			for _, option := range options {
				if choice != "static bucket" && option.Name == "bucket" && option.Reason == "" {
					t.Fatal("server app offered a bucket")
				}
				if option.Name == "local" && !option.Default {
					t.Fatal("default not marked")
				}
			}
			err = s.db.InTx(ctx, func(tx *database.Tx) error {
				return s.InitializeProjectIn(ctx, tx, in.ProjectID, CreationPlacement{Target: selection, Contracts: contracts})
			})
			refused := choice == "private" || choice == "missing" || choice == "bucket"
			if (err != nil) != refused {
				t.Fatalf("choice %q: %v", choice, err)
			}
			deps, err := s.Deployments.ForProject(ctx, in.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			if refused || choice == "none" {
				if len(deps) != 0 {
					t.Fatalf("unexpected placement: %+v", deps)
				}
				return
			}
			target := local.ID
			if choice == "cloud" {
				target = cloud.ID
			}
			if choice == "static bucket" {
				bucket, err := s.Targets.ByName(ctx, "bucket")
				if err != nil {
					t.Fatal(err)
				}
				target = bucket.ID
			}
			if len(deps) != 1 || deps[0].Name != "main" || deps[0].Feed != "main" || !deps[0].FeedConfirmed || deps[0].TargetID != target {
				t.Fatalf("choice %q: %+v", choice, deps)
			}
		})
	}
}
