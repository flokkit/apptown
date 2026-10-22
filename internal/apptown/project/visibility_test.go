package project

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project/internal/projectsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestVisibleProjectsIncludeIdleProjectsAndFenceEmptySets(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	auth := identity.New(db, identity.Config{Projects: NewCatalogue(db)})
	if err := auth.SeedAdministrator(context.Background(), "boss"); err != nil {
		t.Fatal(err)
	}
	var id ids.ID
	for _, slug := range []string{"z-visible", "a-private"} {
		v, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
			return CreateAt(t.Context(), tx, time.Now(), Spec{Slug: slug, Name: slug})
		})
		if err != nil {
			t.Fatal(err)
		}
		if id.IsZero() {
			id = v
		}
	}
	// An eval copy the reader may read is still not listed: it appears
	// under its eval, never as a project.
	copyID, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return CreateAt(t.Context(), tx, time.Now(), Spec{Slug: "y-private-copy", Name: "y-private-copy", Purpose: EvalCopy})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.EnsureUser(context.Background(), "reader"); err != nil {
		t.Fatal(err)
	}
	grant, err := auth.Grant(context.Background(), identity.Spec{SubjectType: "user", Subject: "reader", Capability: string(access.CapProjectRead), Scope: access.ScopeProject, ScopeID: fmt.Sprint(id)}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Grant(context.Background(), identity.Spec{SubjectType: "user", Subject: "reader", Capability: string(access.CapProjectRead), Scope: access.ScopeProject, ScopeID: fmt.Sprint(copyID)}, ""); err != nil {
		t.Fatal(err)
	}
	s := New(db, auth)
	ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "reader"}, Verified: true})
	rows, err := s.Visible(ctx)
	if err != nil || len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("idle project visibility: %+v %v", rows, err)
	}
	for _, idList := range [][]ids.ID{nil, {}} {
		got, err := projectsql.New(db.SQL()).VisibleProjects(ctx, idList)
		if err != nil || len(got) != 0 {
			t.Fatalf("empty SQL scope: %+v %v", got, err)
		}
	}
	for _, in := range []Browse{{Limit: 1}, {Limit: 1, IDs: []ids.ID{idstest.N(2), copyID, id}}, {Limit: 2, Query: "y-private"}, {Limit: 1, Offset: 5}, {Limit: 1, Query: "private"}, {Limit: 1, IDs: []ids.ID{}}} {
		page, err := s.Browse(ctx, in)
		want := int64(1)
		if in.Query == "private" || in.Query == "y-private" || (in.IDs != nil && len(in.IDs) == 0) {
			want = 0
		}
		if err != nil || page.Total != want {
			t.Fatalf("page %+v: %+v %v", in, page, err)
		}
		if in.Offset == 0 && int64(len(page.Projects)) != want {
			t.Fatalf("page lost readable project: %+v", page)
		}
		if in.Offset > 0 && len(page.Projects) != 0 {
			t.Fatalf("offset ignored: %+v", page)
		}
		for _, p := range page.Projects {
			if p.ID != id {
				t.Fatalf("foreign project in page: %+v", page)
			}
		}
	}
	for _, in := range []Browse{{Limit: 0}, {Limit: 101}, {Limit: 1, Offset: -1}} {
		if _, err := s.Browse(ctx, in); err == nil {
			t.Fatalf("invalid page: %+v", in)
		}
	}
	if _, err := auth.Revoke(ctx, grant, "boss"); err != nil {
		t.Fatal(err)
	}
	rows, err = s.Visible(ctx)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("revoked visibility: %+v %v", rows, err)
	}
	if _, err := s.Visible(context.Background()); err == nil {
		t.Fatal("unverified read admitted")
	}
}
