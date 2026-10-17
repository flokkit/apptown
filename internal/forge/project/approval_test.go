package project

import (
	"context"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/ids"
)

func TestApprovalPolicyRequiresCurrentAuthorityAndReceipt(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	authority := identity.New(db, identity.Config{})
	projects := New(db, authority)
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return CreateAt(t.Context(), tx, time.Now(), Spec{Slug: "demo"})
	})
	if err != nil {
		t.Fatal(err)
	}
	pr := access.Principal{Kind: "user", Name: "owner", Via: "session"}
	ctx := access.WithCaller(context.Background(), access.Caller{Principal: pr, Verified: true})
	if err := projects.SetIndependentApproval(ctx, id, true); err == nil {
		t.Fatal("policy changed without authority")
	}
	if p, err := projects.ByID(ctx, id); err != nil || p.RequireIndependentApproval {
		t.Fatalf("denied effect: %+v %v", p, err)
	}
	if _, err := authority.PutGrant(ctx, identity.Spec{SubjectType: "user", Subject: "owner", Role: access.RoleProjectOwner, Scope: access.ScopeProject, ScopeID: id.String()}); err != nil {
		t.Fatal(err)
	}
	restore := db.FailNextStatement("INSERT INTO audit_log")
	err = projects.SetIndependentApproval(ctx, id, true)
	restore()
	if err == nil {
		t.Fatal("policy changed without receipt")
	}
	if p, err := projects.ByID(ctx, id); err != nil || p.RequireIndependentApproval {
		t.Fatalf("partial effect: %+v %v", p, err)
	}
	if err := projects.SetIndependentApproval(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if p, err := projects.ByID(ctx, id); err != nil || !p.RequireIndependentApproval {
		t.Fatalf("policy missing: %+v %v", p, err)
	}
	if err := projects.SetIndependentApproval(access.WithCaller(context.Background(), access.Caller{Principal: pr}), id, false); !errors.Is(err, access.ErrPrincipalRequired) {
		t.Fatalf("unverified mutation: %v", err)
	}
	if err := projects.SetIndependentApproval(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	receipts, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: "project.approval-policy", Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, receipt := range receipts {
		if receipt.Kind == "project.approval-policy" && receipt.Actor == "owner" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("receipts: %d", n)
	}
}
