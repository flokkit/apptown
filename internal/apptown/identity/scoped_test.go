package identity

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestScopedAuthorityRequiresItsServiceAndName(t *testing.T) {
	t.Parallel()
	for _, a := range []access.Authority{Scoped(nil, "work"), Scoped(New(nil, Config{}), "")} {
		if _, err := a.Require(context.Background(), access.Principal{Name: "alice"}, access.Permission{}); !errors.Is(err, access.ErrAuthorityUnavailable) {
			t.Fatalf("unconfigured authority: %v", err)
		}
	}
}

func TestScopedAuthorityRecordsOwnerAndKeepsIdentity(t *testing.T) {
	t.Parallel()
	svc := New(testDB(t), Config{})
	if err := testSaveAccount(svc.db, accountEdit{Name: "alice", Role: "member"}, "setup"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Grant(context.Background(), Spec{SubjectType: "user", Subject: "alice", Capability: string(access.CapProjectRead), Scope: access.ScopeProject, ScopeID: idstest.N(7).String(), By: "setup"}, ""); err != nil {
		t.Fatal(err)
	}
	pr := access.Principal{Kind: "user", Name: "alice", Via: "token"}
	p := access.Permission{Capability: access.CapProjectRead, Resource: access.Resource{Type: "project", ProjectID: idstest.N(7)}, Context: access.Context{Consequential: true, Route: "read"}}
	got, err := Scoped(svc, "work").Require(context.Background(), pr, p)
	if err != nil || got.Name != pr.Name || got.Via != pr.Via {
		t.Fatalf("identity: %+v %v", got, err)
	}
	elsewhere := p
	elsewhere.Resource.ProjectID = idstest.N(8)
	if _, err := Scoped(svc, "work").Require(context.Background(), pr, elsewhere); err == nil {
		t.Fatal("alice read a project she holds nothing on")
	}
	denied := ledger(t, svc.db, "authz.denied")
	if len(denied) != 1 || !strings.Contains(string(denied[0].Payload), `"route":"work:read"`) {
		t.Fatalf("scoped denial: %+v", denied)
	}
	if p.Context.Route != "read" {
		t.Fatal("scoping mutated caller's permission")
	}
}
