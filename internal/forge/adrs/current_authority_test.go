package adrs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
)

// adrAs is the test-side twin of adrContext: an operation context carrying pr.
func adrAs(pr access.Principal) context.Context {
	return access.WithCaller(context.Background(), access.Caller{Principal: pr, Verified: true})
}
func TestADRAuthorityAppliesWithoutRouteGuards(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	accounts := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := accounts.SeedAdministrator(context.Background(), "boss"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"owner", "outsider"} {
		if _, err := accounts.EnsureUser(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	projectID := projectFixture(t, db, "game", "Game")
	open := projectFixture(t, db, "open", "Open")
	for _, role := range []string{access.RoleProjectMaintainer, access.RoleProjectApprover} {
		if _, err := accounts.Grant(context.Background(), identity.Spec{SubjectType: "user", Subject: "owner", Role: role, Scope: access.ScopeProject, ScopeID: projectID.String(), By: "boss"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := accounts.Grant(context.Background(), identity.Spec{SubjectType: "group", Subject: identity.GroupAllUsers, Role: access.RoleProjectMaintainer, Scope: access.ScopeProject, ScopeID: open.String(), By: "boss"}, ""); err != nil {
		t.Fatal(err)
	}
	service := New(db, access.Checked{Authority: identity.Scoped(accounts, "adrs"), Projects: project.New(db, nil)}, nil, nil)
	owner := access.Principal{Kind: "user", Name: "owner", Via: "token"}
	record, err := service.Create(adrAs(owner), Create{ProjectID: projectID, Title: "real decision"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		pr   access.Principal
		code string
	}{
		{access.Principal{Kind: "user", Name: "outsider", Via: "token"}, access.DenyOutsideScope},
		{access.Principal{Kind: "user", Name: "owner", Via: "oauth", Scopes: []string{"forge:read"}}, access.DenyDelegationNarrow},
		{access.Principal{Kind: "instrument", Name: "run/1", Instrument: "run", Via: "run", ProjectID: projectID}, access.DenyDelegationNarrow},
		{access.Principal{Kind: "instrument", Name: "owner via console/c", Person: "owner", Instrument: "console", Via: "console", ProjectID: open}, access.DenyOutsideScope},
	} {
		ctx := adrAs(tc.pr)
		got, err := service.Edit(ctx, Edit{ID: record.ID, Decision: Accept})
		var denied *access.Denied
		if got != nil || !errors.As(err, &denied) || denied.Code != tc.code {
			t.Fatalf("unauthorized %+v: %+v %v", tc.pr, got, err)
		}
	}
	disabled := true
	if err := accounts.SaveUser(context.Background(), identity.AccountChange{Username: "owner", Disabled: &disabled}, access.Principal{Kind: "user", Name: "boss", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Edit(adrAs(owner), Edit{ID: record.ID, Decision: Accept}); err == nil {
		t.Fatal("disabled account edited decision")
	}
	disabled = false
	if err := accounts.SaveUser(context.Background(), identity.AccountChange{Username: "owner", Disabled: &disabled}, access.Principal{Kind: "user", Name: "boss", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, db, "-- name: UserByName :one")
	if _, err := service.Edit(adrAs(owner), Edit{ID: record.ID, Decision: Accept}); !(errors.Is(err, database.ErrInjectedFailure) || err != nil && strings.Contains(err.Error(), "forge_injected_failure")) {
		t.Fatalf("authority read failure was hidden: %v", err)
	}
	if _, err := service.Edit(adrAs(owner), Edit{ID: record.ID, Decision: Accept}); err != nil {
		t.Fatal(err)
	}
	wantCount(t, "owner's update receipts", receiptsBy(t, db, "adr.updated", "owner"), 1)
	grants, err := accounts.GrantsAt(context.Background(), access.ScopeProject, projectID.String())
	if err != nil {
		t.Fatal(err)
	}
	revoked := 0
	for _, grant := range grants {
		if grant.SubjectType == "user" && grant.Subject == "owner" {
			if _, err := accounts.Revoke(context.Background(), grant.ID, "boss"); err != nil {
				t.Fatal(err)
			}
			revoked++
		}
	}
	if revoked != 2 {
		t.Fatalf("expected two owner grants revoked, got %d", revoked)
	}
	if _, err := service.Edit(adrAs(owner), Edit{ID: record.ID, Decision: Deprecate}); err == nil {
		t.Fatal("revoked authority edited decision")
	}
}
