package identity

import (
	"context"
	"errors"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/ids/idstest"
)

// Construction is independent of Forge and performs no database work.
func TestServiceConstructionAndCheckedAuthority(t *testing.T) {
	t.Parallel()
	svc := New(nil, Config{})
	var authority access.Authority = svc
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := authority.Require(ctx, access.Principal{Kind: "user", Name: "alice"}, access.Permission{Capability: access.CapProjectRead}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled authority: %v", err)
	}
	if _, err := authority.Require(context.Background(), access.Principal{}, access.Permission{}); !errors.Is(err, access.ErrPrincipalRequired) {
		t.Fatalf("missing identity: %v", err)
	}
}

func TestServiceReadsCurrentGrantsAndRequiresReceipt(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	svc := New(db, Config{})
	if err := testSaveAccount(svc.db, accountEdit{Name: "alice", Role: "member"}, "setup"); err != nil {
		t.Fatal(err)
	}
	id, err := svc.Grant(context.Background(), Spec{SubjectType: "user", Subject: "alice", Capability: string(access.CapProjectRead), Scope: access.ScopeProject, ScopeID: idstest.N(7).String(), By: "setup"}, "")
	if err != nil {
		t.Fatal(err)
	}
	pr := access.Principal{Kind: "user", Name: "alice", Via: "token"}
	p := access.Permission{Capability: access.CapProjectRead, Resource: access.Resource{Type: "project", ProjectID: idstest.N(7), ProjectName: "example"}, Context: access.Context{Consequential: true}}
	if got, err := svc.Require(context.Background(), pr, p); err != nil || got.Name != pr.Name {
		t.Fatalf("current grant: %+v %v", got, err)
	}
	restore := db.FailNextStatement("-- name: InsertDecision")
	if _, err := svc.Require(context.Background(), pr, p); err == nil {
		t.Fatal("a missing required receipt admitted the operation")
	}
	restore()
	if _, err := svc.Revoke(context.Background(), id, "setup"); err != nil {
		t.Fatal(err)
	}
	var denied *access.Denied
	if _, err := svc.Require(context.Background(), pr, p); !errors.As(err, &denied) {
		t.Fatalf("revocation was not observed: %v", err)
	}
	restore = db.FailNextStatement("-- name: UserByName :one")
	if _, err := svc.Explain(context.Background(), pr, p); err == nil || errors.As(err, &denied) {
		t.Fatalf("unreadable account became a policy result: %v", err)
	}
	restore()
}

func TestServiceChecksConsoleOwner(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	svc := New(db, Config{})
	if err := testSaveAccount(svc.db, accountEdit{Name: "alice", Role: "member"}, "setup"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Grant(context.Background(), Spec{SubjectType: "user", Subject: "alice", Capability: string(access.CapProjectRead), Scope: access.ScopeProject, ScopeID: idstest.N(7).String(), By: "setup"}, ""); err != nil {
		t.Fatal(err)
	}
	disabled := true
	if err := testSaveAccount(svc.db, accountEdit{Name: "alice", Role: "member", Disabled: &disabled}, "setup"); err != nil {
		t.Fatal(err)
	}
	pr := access.Principal{Kind: "instrument", Name: "alice via console/c", Person: "alice", Instrument: "console", ProjectID: idstest.N(7)}
	var denied *access.Denied
	if _, err := svc.Require(context.Background(), pr, access.Permission{Capability: access.CapProjectRead, Resource: access.Resource{Type: "project", ProjectID: idstest.N(7)}}); !errors.As(err, &denied) || denied.Code != access.DenyAccountDisabled {
		t.Fatalf("disabled console owner: %v", err)
	}
}
