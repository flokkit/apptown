package projectlifecycle

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

func requestedOwner(t *testing.T) (ownerFixture, context.Context) {
	t.Helper()
	f := newOwnerFixture(t, false)
	if err := f.identity.SeedAdministrator(t.Context(), "boss"); err != nil {
		t.Fatal(err)
	}
	if err := f.identity.EnsureDefaults(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	return f, access.WithCaller(t.Context(), access.Caller{Verified: true, Receipt: "boss", Principal: testAdmittedPrincipal(t, f.identity, "boss")})
}
func requestID() ids.ID { return ids.New(time.Now()) }

func TestCreationRequestReplaysWholeIntentAndPreservesOriginalResult(t *testing.T) {
	t.Parallel()
	f, ctx := requestedOwner(t)
	in := Draft{RequestID: requestID(), Slug: "ledger", Name: "Ledger", Setup: &CreationSetup{Origin: "empty", Services: []CreationService{{Capability: "postgres", Params: map[string]string{"version": "16"}}}}}
	first, err := f.lifecycle.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := f.lifecycle.Create(ctx, in)
	if err != nil || retry.ID != first.ID || retry.ServingLabel != "ledger" {
		t.Fatalf("replay: %+v %v", retry, err)
	}
	changed := in
	setup := *in.Setup
	changed.Setup = &setup
	changed.Setup.Services = []CreationService{{Capability: "postgres", Params: map[string]string{"version": "17"}}}
	if _, err := f.lifecycle.Create(ctx, changed); !errors.Is(err, project.ErrRequestReused) {
		t.Fatalf("changed setup accepted: %v", err)
	}
	next, rev := "accounts", first.SettingsRevision
	renamed, err := f.lifecycle.UpdateSettings(ctx, first.ID, project.SettingsPatch{ExpectedRevision: &rev, Slug: &next}, "untrusted")
	if err != nil {
		t.Fatal(err)
	}
	retry, err = f.lifecycle.Create(ctx, in)
	if err != nil || retry.ID != first.ID || retry.Slug != "ledger" {
		t.Fatalf("original committed result: %+v %v", retry, err)
	}
	if p, err := f.lifecycle.projects.BySlug(ctx, "accounts"); err != nil || p.ID != first.ID || p.ServingLabel != "ledger" {
		t.Fatalf("new address: %+v %v", p, err)
	}
	if p, err := f.lifecycle.projects.BySlug(ctx, "ledger"); err == nil {
		t.Fatalf("the old address still names %+v", p)
	}
	if renamed.ServingLabel != "ledger" {
		t.Fatal("rename changed serving identity")
	}
	if got := len(lifecycleReceipts(t, f.db, "project.created")); got != 1 {
		t.Fatalf("created receipts %d", got)
	}
}

func TestConcurrentCreationRequestsChooseOneIdentityAndCollisionOnce(t *testing.T) {
	t.Parallel()
	f, ctx := requestedOwner(t)
	if _, err := f.lifecycle.Create(ctx, Draft{RequestID: requestID(), Slug: "ledger", Name: "Existing"}); err != nil {
		t.Fatal(err)
	}
	in := Draft{RequestID: requestID(), Slug: "ledger", Name: "New", AutomaticAddress: true}
	const n = 6
	results := make([]*project.Project, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() { defer wg.Done(); results[i], errs[i] = f.lifecycle.Create(ctx, in) }()
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("request %d: %v", i, errs[i])
		}
		if results[i].ID != results[0].ID || results[i].Slug != results[0].Slug || results[i].Slug == "ledger" {
			t.Fatalf("inconsistent allocation: %+v %+v", results[0], results[i])
		}
	}
	explicit := in
	explicit.RequestID = requestID()
	explicit.AutomaticAddress = false
	if _, err := f.lifecycle.Create(ctx, explicit); !errors.Is(err, project.ErrSlugTaken) {
		t.Fatalf("explicit collision: %v", err)
	}
}

func TestCreationResultFailureRollsBackAddressOwnershipAndAudit(t *testing.T) {
	t.Parallel()
	for _, statement := range []string{"INSERT INTO audit_log", "UPDATE project_creation_requests"} {
		t.Run(statement, func(t *testing.T) {
			f, ctx := requestedOwner(t)
			in := Draft{RequestID: requestID(), Slug: "atomic-request", Name: "Atomic"}
			restore := dbtest.NextStatementFailure(f.db, statement)
			_, err := f.lifecycle.Create(ctx, in)
			restore()
			if err == nil {
				t.Fatal("accepted missing receipt/result")
			}
			if slugTaken(t, f.db, in.Slug) {
				t.Fatal("failed request left project")
			}
			if got := len(lifecycleReceipts(t, f.db, "project.created")); got != 0 {
				t.Fatalf("failed request left %d receipts", got)
			}
			p, err := f.lifecycle.Create(ctx, in)
			if err != nil || p.Slug != in.Slug {
				t.Fatalf("retry after rollback: %+v %v", p, err)
			}
		})
	}
}

// A rename and its receipt commit together: a receipt that cannot be
// written leaves the project where it was.
func TestRenameReceiptFailureLeavesTheProjectWhereItWas(t *testing.T) {
	t.Parallel()
	f, ctx := requestedOwner(t)
	p, err := f.lifecycle.Create(ctx, Draft{RequestID: requestID(), Slug: "original", Name: "Original"})
	if err != nil {
		t.Fatal(err)
	}
	next, rev := "next", p.SettingsRevision
	patch := project.SettingsPatch{ExpectedRevision: &rev, Slug: &next}
	restore := dbtest.NextStatementFailure(f.db, "INSERT INTO audit_log")
	_, err = f.lifecycle.UpdateSettings(ctx, p.ID, patch, "boss")
	restore()
	if err == nil {
		t.Fatal("rename accepted missing receipt")
	}
	held, err := f.lifecycle.projects.ByID(ctx, p.ID)
	if err != nil || held.Slug != "original" || held.SettingsRevision != rev {
		t.Fatalf("failed rename wrote: %+v %v", held, err)
	}
	if _, err := f.lifecycle.UpdateSettings(ctx, p.ID, patch, "boss"); err != nil {
		t.Fatal(err)
	}
	if got := len(lifecycleReceipts(t, f.db, "project.renamed")); got != 1 {
		t.Fatalf("rename receipts %d", got)
	}
}

// An address a settings edit cannot route is refused before anything is
// written.
func TestSettingsRefuseAnAddressOfTheWrongShape(t *testing.T) {
	t.Parallel()
	f, ctx := requestedOwner(t)
	p, err := f.lifecycle.Create(ctx, Draft{RequestID: requestID(), Slug: "shaped", Name: "Shaped"})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "a/b", "Upper"} {
		if _, err := f.lifecycle.UpdateSettings(ctx, p.ID, project.SettingsPatch{ExpectedRevision: &p.SettingsRevision, Slug: &bad}, "boss"); !errors.Is(err, project.ErrSlug) {
			t.Errorf("address %q: %v", bad, err)
		}
	}
	held, err := f.lifecycle.projects.ByID(ctx, p.ID)
	if err != nil || held.Slug != "shaped" || held.SettingsRevision != p.SettingsRevision {
		t.Fatalf("a refused address wrote: %+v %v", held, err)
	}
}

func TestCreationRequestRefusesChangedServiceSelectionPresence(t *testing.T) {
	t.Parallel()
	f, ctx := requestedOwner(t)
	in := Draft{RequestID: requestID(), Slug: "selection", Name: "Selection", Setup: &CreationSetup{Origin: "empty"}}
	if _, err := f.lifecycle.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.Setup = &CreationSetup{Origin: "empty", Services: []CreationService{}}
	if _, err := f.lifecycle.Create(ctx, in); !errors.Is(err, project.ErrRequestReused) {
		t.Fatalf("changed selection presence accepted: %v", err)
	}
}

func TestCreationReplayChecksCurrentActorAndProjectLifecycle(t *testing.T) {
	t.Parallel()
	f, ctx := requestedOwner(t)
	in := Draft{RequestID: requestID(), Slug: "current-authority", Name: "Current"}
	p, err := f.lifecycle.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.lifecycle.Create(context.Background(), in); !errors.Is(err, access.ErrPrincipalRequired) {
		t.Fatalf("unverified replay: %v", err)
	}
	if err := f.db.InTx(ctx, func(tx *database.Tx) error {
		return project.MarkDeletingIn(ctx, tx, p.ID, access.Actor{Name: "boss"}, ids.Nil, false, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.lifecycle.Create(ctx, in); !errors.Is(err, project.ErrNotActive) {
		t.Fatalf("inactive replay: %v", err)
	}
}

func TestCreationReplayKeepsResolvedGroupAndRejectsDisabledActor(t *testing.T) {
	t.Parallel()
	f, ctx := requestedOwner(t)
	boss := testAdmittedPrincipal(t, f.identity, "boss")
	in := Draft{RequestID: requestID(), Slug: "group-request", Name: "Group request"}
	p, err := f.lifecycle.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.identity.SaveGroup(ctx, identity.GroupChange{Name: "later-group"}, boss); err != nil {
		t.Fatal(err)
	}
	group := "later-group"
	if err := f.identity.SaveUser(ctx, identity.AccountChange{Username: "boss", DefaultGroup: &group}, boss); err != nil {
		t.Fatal(err)
	}
	replay, err := f.lifecycle.Create(ctx, in)
	if err != nil || replay.ID != p.ID || replay.OwnerGroup != p.OwnerGroup {
		t.Fatalf("default change altered replay: %+v %v", replay, err)
	}
	if err := f.identity.SeedAdministrator(ctx, "second-admin"); err != nil {
		t.Fatal(err)
	}
	disabled := true
	if err := f.identity.SaveUser(ctx, identity.AccountChange{Username: "boss", Disabled: &disabled}, testAdmittedPrincipal(t, f.identity, "second-admin")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.lifecycle.Create(ctx, in); err == nil {
		t.Fatal("disabled actor replayed a result")
	}
}

// A rename moves a project's address: the address it leaves is free for
// another project, except its first, which its apps are served under and
// which stays with it until it is purged.
func TestAVacatedAddressIsFreeButAFirstAddressStaysWithItsProject(t *testing.T) {
	t.Parallel()
	f, ctx := requestedOwner(t)
	a, err := f.lifecycle.Create(ctx, Draft{RequestID: requestID(), Slug: "ledger", Name: "Ledger"})
	if err != nil {
		t.Fatal(err)
	}
	for _, next := range []string{"accounts", "books"} {
		a, err = f.lifecycle.UpdateSettings(ctx, a.ID, project.SettingsPatch{ExpectedRevision: &a.SettingsRevision, Slug: &next}, "boss")
		if err != nil {
			t.Fatal(err)
		}
	}
	b, err := f.lifecycle.Create(ctx, Draft{RequestID: requestID(), Slug: "accounts", Name: "Accounts"})
	if err != nil || b.Slug != "accounts" || b.ServingLabel != "accounts" {
		t.Fatalf("vacated address: %+v %v", b, err)
	}
	if found, err := f.lifecycle.projects.BySlug(ctx, "accounts"); err != nil || found.ID != b.ID {
		t.Fatalf("accounts names %+v %v; want the new project", found, err)
	}
	if _, err := f.lifecycle.Create(ctx, Draft{RequestID: requestID(), Slug: "ledger", Name: "Other"}); !errors.Is(err, project.ErrSlugTaken) {
		t.Fatalf("first address reassigned: %v", err)
	}
	first := "ledger"
	if _, err := f.lifecycle.UpdateSettings(ctx, b.ID, project.SettingsPatch{ExpectedRevision: &b.SettingsRevision, Slug: &first}, "boss"); !errors.Is(err, project.ErrSlugTaken) {
		t.Fatalf("first address taken by a rename: %v", err)
	}
	if served, err := f.lifecycle.projects.ByServingLabel(ctx, "ledger"); err != nil || served.ID != a.ID {
		t.Fatalf("serving label ledger: %+v %v", served, err)
	}
	if back, err := f.lifecycle.UpdateSettings(ctx, a.ID, project.SettingsPatch{ExpectedRevision: &a.SettingsRevision, Slug: &first}, "boss"); err != nil || back.Slug != "ledger" {
		t.Fatalf("its own first address: %+v %v", back, err)
	}
}

func TestConcurrentCreationAndRenameCannotShareAnAddress(t *testing.T) {
	t.Parallel()
	f, ctx := requestedOwner(t)
	p, err := f.lifecycle.Create(ctx, Draft{RequestID: requestID(), Slug: "rename-source", Name: "Source"})
	if err != nil {
		t.Fatal(err)
	}
	next := "contended-address"
	var renamed, created *project.Project
	var renameErr, createErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		renamed, renameErr = f.lifecycle.UpdateSettings(ctx, p.ID, project.SettingsPatch{ExpectedRevision: &p.SettingsRevision, Slug: &next}, "boss")
	}()
	go func() {
		defer wg.Done()
		created, createErr = f.lifecycle.Create(ctx, Draft{RequestID: requestID(), Slug: next, Name: "Contender"})
	}()
	wg.Wait()
	if (renameErr == nil) == (createErr == nil) {
		t.Fatalf("must admit exactly one assignment: rename=%+v/%v create=%+v/%v", renamed, renameErr, created, createErr)
	}
	winner := created
	if renameErr == nil {
		winner = renamed
	}
	found, err := f.lifecycle.projects.BySlug(ctx, next)
	if err != nil || found.ID != winner.ID {
		t.Fatalf("assigned wrong project: %+v %v", found, err)
	}
	if original, err := f.lifecycle.projects.BySlug(ctx, "rename-source"); (renameErr == nil) == (err == nil) || (err == nil && original.ID != p.ID) {
		t.Fatalf("rename-source after the contest: %+v %v (rename %v)", original, err, renameErr)
	}
}

func TestPurgedCreationNeverReplaysAndFreesItsAddress(t *testing.T) {
	t.Parallel()
	f, ctx := requestedOwner(t)
	in := Draft{RequestID: requestID(), Slug: "purged-request", Name: "Purged"}
	p, err := f.lifecycle.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// This is the final project-owner participant after other owners' purge;
	// the request must survive removal of its project row; the address is free.
	if err := f.lifecycle.projects.Remove(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.lifecycle.Create(ctx, in); err == nil {
		t.Fatal("purged project replayed its historical result")
	}
	changed := in
	changed.Slug = "unused-after-purge"
	if _, err := f.lifecycle.Create(ctx, changed); !errors.Is(err, project.ErrRequestReused) {
		t.Fatalf("purge lost the retained request identity: %v", err)
	}
	another := in
	another.RequestID = requestID()
	if again, err := f.lifecycle.Create(ctx, another); err != nil || again.ID == p.ID || again.Slug != in.Slug {
		t.Fatalf("purged address: %+v %v", again, err)
	}
}
