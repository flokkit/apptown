package identity

import (
	"context"
	"errors"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

type authorityFunc func(context.Context, access.Principal, access.Permission) (access.Principal, error)

func (f authorityFunc) Require(ctx context.Context, pr access.Principal, p access.Permission) (access.Principal, error) {
	return f(ctx, pr, p)
}

type resourceFunc func(context.Context, ids.ID) (access.Resource, error)

func (f resourceFunc) AuthorityResource(ctx context.Context, id ids.ID) (access.Resource, error) {
	return f(ctx, id)
}

func TestCallerCopiesScopesAtBothBoundaries(t *testing.T) {
	scopes := []string{"apptown:user:read"}
	ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Name: "person", Scopes: scopes}, Verified: true, Receipt: "client"})
	scopes[0] = "changed"
	c, _ := access.CallerFrom(ctx)
	c.Principal.Scopes[0] = "changed again"
	c, _ = access.CallerFrom(ctx)
	if c.Principal.Scopes[0] != "apptown:user:read" || !c.Verified || c.Receipt != "client" {
		t.Fatalf("caller aliases: %+v", c)
	}
}
func TestCheckedCompletesOnlyContainingResourceFacts(t *testing.T) {
	ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "person"}, Verified: true, Receipt: "client"})
	p := access.Permission{Capability: access.CapReleasePromote, Resource: access.Resource{Type: "release-target", ID: "production", ProjectID: idstest.N(7), ChangeID: idstest.N(8), RunID: idstest.N(9)}, Context: access.Context{Route: "release", Consequential: true}}
	checked := access.Checked{Projects: resourceFunc(func(context.Context, ids.ID) (access.Resource, error) {
		return access.Resource{Type: "project", ID: idstest.N(7).String(), ProjectName: "Project", Group: "team"}, nil
	}), Authority: authorityFunc(func(_ context.Context, pr access.Principal, got access.Permission) (access.Principal, error) {
		want := p
		want.Resource.ProjectName = "Project"
		want.Resource.Group = "team"
		if got != want {
			t.Fatalf("lost resource or receipt: %+v", got)
		}
		return pr, nil
	})}
	c, err := checked.Require(ctx, p)
	if err != nil || c.Receipt != "client" {
		t.Fatalf("caller: %+v %v", c, err)
	}
	failure := errors.New("facts unavailable")
	checked.Projects = resourceFunc(func(context.Context, ids.ID) (access.Resource, error) { return access.Resource{}, failure })
	if _, err := checked.Require(ctx, p); !errors.Is(err, failure) {
		t.Fatalf("lost read failure: %v", err)
	}
}
func TestCheckedRequiresExplicitVerifiedCaller(t *testing.T) {
	for _, c := range []access.Caller{{}, {Principal: access.Principal{Name: "root"}}, {Verified: true}} {
		_, err := (access.Checked{}).Require(access.WithCaller(context.Background(), c), access.Permission{})
		if !errors.Is(err, access.ErrPrincipalRequired) {
			t.Fatalf("admitted %+v: %v", c, err)
		}
	}
}
