package adoption

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

func TestFleetProjectReadsEnforceVisibilityAndEmptySQLScopes(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	auth := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := auth.SeedAdministrator(context.Background(), "boss"); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.EnsureUser(context.Background(), "reader"); err != nil {
		t.Fatal(err)
	}
	ways := wow.New(db, wow.Dependencies{})
	if _, err := ways.PublishPack(t.Context(), "fleet", "Fleet", `{}`, ""); err != nil {
		t.Fatal(err)
	}
	pack, err := ways.Pack(t.Context(), "fleet")
	if err != nil {
		t.Fatal(err)
	}
	var projectIDs []ids.ID
	for _, slug := range []string{"visible", "private"} {
		id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
			id, err := project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug})
			if err != nil {
				return ids.Nil, err
			}
			return id, InitializeIn(t.Context(), tx, id, 0, "")
		})
		if err != nil {
			t.Fatal(err)
		}
		projectIDs = append(projectIDs, id)
	}
	grant, err := auth.Grant(context.Background(), identity.Spec{SubjectType: "user", Subject: "reader", Capability: string(access.CapProjectRead), Scope: access.ScopeProject, ScopeID: projectIDs[0].String()}, "")
	if err != nil {
		t.Fatal(err)
	}
	// An eval copy the reader may read is in neither list: a copy appears
	// under its eval, never in the fleet.
	copyID, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		id, err := project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "visible-copy", Purpose: project.EvalCopy})
		if err != nil {
			return ids.Nil, err
		}
		return id, InitializeIn(t.Context(), tx, id, 0, "")
	})
	if err != nil {
		t.Fatal(err)
	}
	projectIDs = append(projectIDs, copyID)
	if _, err := auth.Grant(context.Background(), identity.Spec{SubjectType: "user", Subject: "reader", Capability: string(access.CapProjectRead), Scope: access.ScopeProject, ScopeID: fmt.Sprint(copyID)}, ""); err != nil {
		t.Fatal(err)
	}
	s := New(db, Dependencies{Authority: access.Checked{Authority: auth, Projects: project.New(db, auth)}})
	ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "reader"}, Verified: true})

	before, err := s.Fleet(ctx)
	if err != nil || len(before.On) != 0 {
		t.Fatalf("no memberships yet: %+v %v", before, err)
	}
	for _, id := range projectIDs {
		if err := db.InTx(ctx, func(tx *database.Tx) error {
			return SetMembershipIn(ctx, tx, id, []Membership{{Pack: "fleet", Version: 1}}, "test", "boss", time.Now())
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Fleet(ctx)
	key := PackVersion{PackID: pack.ID, Version: 1}
	if err != nil || len(got.On) != 1 || len(got.On[key]) != 1 || got.On[key][0] != "visible" {
		t.Fatalf("memberships: %+v %v", got, err)
	}
	for _, scope := range [][]ids.ID{nil, {}} {
		got, err := readFleet(ctx, db, scope)
		if err != nil || got.On == nil || len(got.On) != 0 {
			t.Fatalf("empty SQL scope: %+v %v", got, err)
		}
	}
	if _, err := auth.Revoke(ctx, grant, "boss"); err != nil {
		t.Fatal(err)
	}
	got, err = s.Fleet(ctx)
	if err != nil || len(got.On) != 0 {
		t.Fatalf("revoked: %+v %v", got, err)
	}
	if _, err := s.Fleet(context.Background()); err == nil {
		t.Fatal("unverified fleet read admitted")
	}
	dbtest.FailNextStatement(t, db, "-- name: ProjectAuthorityCatalogue")
	if got, err := s.Fleet(ctx); err == nil || got.On != nil {
		t.Fatalf("failed visibility: %+v %v", got, err)
	}
	dbtest.FailNextStatement(t, db, "-- name: FleetPackMemberships")
	if got, err := readFleet(ctx, db, projectIDs); err == nil || got.On != nil {
		t.Fatalf("failed memberships read: %+v %v", got, err)
	}
}

// A new pack version is tried by default on the projects that hold the pack
// at an earlier version: an ordinary project with a repository. One on the
// version or a later one, one not on the pack, an eval copy and a project
// without a repository are not eligible; the caller's read is limited to
// projects they may read. (Whether it is active is the project owner's
// answer, asked by the what-if's admission.) Kills: "below" widened to the
// version itself or narrowed to the recommended one; eval copies or
// repository-less projects let in; the visibility filter dropped.
func TestTheProjectsANewPackVersionIsTriedOn(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	auth := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if _, err := auth.EnsureUser(context.Background(), "reader"); err != nil {
		t.Fatal(err)
	}
	ways := wow.New(db, wow.Dependencies{})
	for _, spec := range []string{`{}`, `{"toolchain":"go"}`, `{"toolchain":"node"}`} {
		if _, err := ways.PublishPack(t.Context(), "team", "Team", spec, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ways.PublishPack(t.Context(), "other", "Other", `{}`, ""); err != nil {
		t.Fatal(err)
	}
	byName := map[string]ids.ID{}
	for _, p := range []struct {
		slug, branch string
		purpose      project.Purpose
		pack         string
		version      int64
	}{
		{"behind", "main", "", "team", 1}, {"also-behind", "main", "", "team", 1}, {"on-it", "main", "", "team", 2}, {"ahead", "main", "", "team", 3},
		{"elsewhere", "main", "", "other", 1}, {"copy", "main", project.EvalCopy, "team", 1}, {"no-repo", "", "", "team", 1},
	} {
		id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
			id, err := project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: p.slug, Purpose: p.purpose})
			if err != nil {
				return ids.Nil, err
			}
			if p.branch != "" {
				if err := project.SetDefaultBranchIn(t.Context(), tx, id, p.branch); err != nil {
					return ids.Nil, err
				}
			}
			return id, SetMembershipIn(t.Context(), tx, id, []Membership{{Pack: p.pack, Version: p.version}}, "test", "boss", time.Now())
		})
		if err != nil {
			t.Fatal(err)
		}
		byName[p.slug] = id
	}
	got, err := ProjectsBelowIn(t.Context(), db, "team", 2)
	if err != nil || !slices.Equal(got, []ProjectRef{{byName["also-behind"], "also-behind"}, {byName["behind"], "behind"}}) {
		t.Fatalf("eligible for team v2: %v %v", got, err)
	}
	if got, err := ProjectsBelowIn(t.Context(), db, "team", 1); err != nil || len(got) != 0 {
		t.Fatalf("nothing is below a first version: %v %v", got, err)
	}
	if _, err := auth.Grant(context.Background(), identity.Spec{SubjectType: "user", Subject: "reader", Capability: string(access.CapProjectRead), Scope: access.ScopeProject, ScopeID: fmt.Sprint(byName["behind"])}, ""); err != nil {
		t.Fatal(err)
	}
	s := New(db, Dependencies{Authority: access.Checked{Authority: auth, Projects: project.New(db, auth)}})
	ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "reader"}, Verified: true})
	if mine, err := s.ProjectsBelow(ctx, "team", 3); err != nil || !slices.Equal(mine, []ProjectRef{{byName["behind"], "behind"}}) {
		t.Fatalf("the reader's eligible projects: %v %v", mine, err)
	}
}
