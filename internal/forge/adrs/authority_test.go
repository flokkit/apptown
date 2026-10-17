package adrs

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/adrs/internal/adrssql"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestPublicOperationsRequireExplicitAuthority(t *testing.T) {
	t.Parallel()
	s, p := fixture(t)
	v := create(t, s, p, "record")
	for _, auth := range []access.ReadAuthorizer{nil, permitFunc(func(context.Context, access.Permission) (string, error) { return "", nil }), permitFunc(func(context.Context, access.Permission) (string, error) { return "", ErrCallerRequired })} {
		denied := New(s.records.db, auth, nil, nil)
		operations := []func() error{
			func() error { _, err := denied.Get(testCaller(), v.ID); return err },
			func() error { _, err := denied.List(testCaller(), Filter{ProjectID: p}); return err },
			func() error {
				_, err := denied.Create(testCaller(), Create{ProjectID: p, Title: "unverified"})
				return err
			},
			func() error {
				_, err := denied.Edit(testCaller(), Edit{ID: v.ID, Decision: Accept})
				return err
			},
			func() error { _, err := denied.ProposeSuccessor(testCaller(), Successor{ID: v.ID}); return err },
			func() error { _, err := denied.ForChange(testCaller(), p, idstest.N(1)); return err },
			func() error { _, err := denied.Counts(testCaller(), []ids.ID{p}); return err },
		}
		before := snapshot(t, s.records.db)
		for _, op := range operations {
			if err := op(); !errors.Is(err, ErrCallerRequired) {
				t.Fatalf("missing authority admitted operation: %v", err)
			}
		}
		if after := snapshot(t, s.records.db); after != before {
			t.Fatal("unverified operation changed records")
		}
	}
	var seen []access.Permission
	denied := New(s.records.db, permitFunc(func(_ context.Context, req access.Permission) (string, error) {
		seen = append(seen, req)
		return "", &access.Denied{Code: "scope", Reason: "outside current scope"}
	}), nil, nil)
	if got, err := denied.Edit(testCaller(), Edit{ID: v.ID, Decision: Accept}); got != nil || err == nil {
		t.Fatalf("denied edit: %+v %v", got, err)
	}
	if len(seen) != 1 || seen[0].Capability != access.CapChangeAttach || seen[0].Resource.ProjectID != p {
		t.Fatalf("authority did not receive owned scope: %+v", seen)
	}
}
func TestChangedScopeAfterAuthorityCannotMoveTheWrite(t *testing.T) {
	t.Parallel()
	s, p := fixture(t)
	v := create(t, s, p, "record")
	other := projectFixture(t, s.records.db, "other", "Other")
	gated := New(s.records.db, permitFunc(func(_ context.Context, req access.Permission) (string, error) {
		if req.Capability == access.CapChangeAttach {
			if req.Resource.ProjectID != p {
				t.Fatalf("wrong admission scope: %+v", req)
			}
			// sql-guarantee: Move the same ADR after authority admission; EditADR guards project identity and no owner mutation transfers an ADR between projects.
			completionExec(t, s.records.db, `UPDATE adrs SET project_id=? WHERE id=?`, other, v.ID)
		}
		return "alice", nil
	}), nil, nil)
	if got, err := gated.Edit(testCaller(), Edit{ID: v.ID, Decision: Accept}); got != nil || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("permission followed a moved record: %+v %v", got, err)
	}
	moved, err := adrssql.New(s.records.db.SQL()).ADRByID(context.Background(), adrssql.ADRByIDParams{ID: v.ID, ProjectID: other})
	if err != nil || moved.Adr.Status != Draft.String() {
		t.Fatalf("moved record changed: %+v %v", moved.Adr, err)
	}
	wantCount(t, "update receipts", receiptsBy(t, s.records.db, "adr.updated", ""), 0)
}

func TestRoutingScopeRequiresVerifiedCaller(t *testing.T) {
	t.Parallel()
	s, p := fixture(t)
	v := create(t, s, p, "scope")
	for _, ctx := range []context.Context{context.Background(), access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "alice"}})} {
		if _, err := s.ProjectID(ctx, v.ID); !errors.Is(err, ErrCallerRequired) {
			t.Fatalf("unverified scope lookup: %v", err)
		}
	}
}
