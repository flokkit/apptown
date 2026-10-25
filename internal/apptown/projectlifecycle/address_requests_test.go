package projectlifecycle

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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
	renamed, err := f.lifecycle.UpdateSettings(ctx, first.ID, project.SettingsPatch{RequestID: requestID(), ExpectedRevision: &rev, Slug: &next}, "untrusted")
	if err != nil {
		t.Fatal(err)
	}
	retry, err = f.lifecycle.Create(ctx, in)
	if err != nil || retry.ID != first.ID || retry.Slug != "ledger" {
		t.Fatalf("original committed result: %+v %v", retry, err)
	}
	for _, address := range []string{"ledger", "accounts"} {
		p, err := f.lifecycle.projects.BySlug(ctx, address)
		if err != nil || p.ID != first.ID || p.Slug != "accounts" || p.ServingLabel != "ledger" {
			t.Fatalf("alias %s: %+v %v", address, p, err)
		}
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
			restore := f.db.FailNextStatement(statement)
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

func TestRenameReceiptFailureAndReplayKeepPermanentAliases(t *testing.T) {
	t.Parallel()
	f, ctx := requestedOwner(t)
	p, err := f.lifecycle.Create(ctx, Draft{RequestID: requestID(), Slug: "original", Name: "Original"})
	if err != nil {
		t.Fatal(err)
	}
	next, rev := "next", p.SettingsRevision
	patch := project.SettingsPatch{RequestID: requestID(), ExpectedRevision: &rev, Slug: &next}
	restore := f.db.FailNextStatement("INSERT INTO audit_log")
	_, err = f.lifecycle.UpdateSettings(ctx, p.ID, patch, "boss")
	restore()
	if err == nil {
		t.Fatal("rename accepted missing receipt")
	}
	held, err := f.lifecycle.projects.ByID(ctx, p.ID)
	if err != nil || held.Slug != "original" || held.SettingsRevision != rev {
		t.Fatalf("failed rename wrote: %+v %v", held, err)
	}
	renamed, err := f.lifecycle.UpdateSettings(ctx, p.ID, patch, "boss")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.lifecycle.UpdateSettings(ctx, p.ID, patch, "boss")
	if err != nil || replay.SettingsRevision != renamed.SettingsRevision {
		t.Fatalf("rename replay: %+v %v", replay, err)
	}
	other := "different"
	patch.Slug = &other
	if _, err := f.lifecycle.UpdateSettings(ctx, p.ID, patch, "boss"); !errors.Is(err, project.ErrRequestReused) {
		t.Fatalf("changed rename accepted: %v", err)
	}
	if _, err := f.lifecycle.Create(ctx, Draft{RequestID: requestID(), Slug: "original", Name: "Other"}); !errors.Is(err, project.ErrSlugTaken) {
		t.Fatalf("old address reassigned: %v", err)
	}
	if got := len(lifecycleReceipts(t, f.db, "project.renamed")); got != 1 {
		t.Fatalf("rename receipts %d", got)
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

func TestRenameCanReactivateOnlyItsOwnPermanentAlias(t *testing.T) {
	t.Parallel()
	f, ctx := requestedOwner(t)
	p, err := f.lifecycle.Create(ctx, Draft{RequestID: requestID(), Slug: "first-address", Name: "First"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.lifecycle.Create(ctx, Draft{RequestID: requestID(), Slug: "other-address", Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	next := "second-address"
	p, err = f.lifecycle.UpdateSettings(ctx, p.ID, project.SettingsPatch{RequestID: requestID(), ExpectedRevision: &p.SettingsRevision, Slug: &next}, "boss")
	if err != nil {
		t.Fatal(err)
	}
	original := "first-address"
	if _, err := f.lifecycle.UpdateSettings(ctx, other.ID, project.SettingsPatch{RequestID: requestID(), ExpectedRevision: &other.SettingsRevision, Slug: &original}, "boss"); !errors.Is(err, project.ErrSlugTaken) {
		t.Fatalf("another project claimed old alias: %v", err)
	}
	p, err = f.lifecycle.UpdateSettings(ctx, p.ID, project.SettingsPatch{RequestID: requestID(), ExpectedRevision: &p.SettingsRevision, Slug: &original}, "boss")
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{original, next} {
		found, err := f.lifecycle.projects.BySlug(ctx, address)
		if err != nil || found.ID != p.ID || found.Slug != original {
			t.Fatalf("reactivated %s: %+v %v", address, found, err)
		}
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
		renamed, renameErr = f.lifecycle.UpdateSettings(ctx, p.ID, project.SettingsPatch{RequestID: requestID(), ExpectedRevision: &p.SettingsRevision, Slug: &next}, "boss")
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
	original, err := f.lifecycle.projects.BySlug(ctx, "rename-source")
	if err != nil || original.ID != p.ID {
		t.Fatalf("original alias changed owner: %+v %v", original, err)
	}
}

func TestPurgedCreationNeverReplaysOrReassignsItsAddress(t *testing.T) {
	t.Parallel()
	f, ctx := requestedOwner(t)
	in := Draft{RequestID: requestID(), Slug: "purged-request", Name: "Purged"}
	p, err := f.lifecycle.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// This is the final project-owner participant after other owners' purge;
	// the request and address must survive removal of their project row.
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
	if _, err := f.lifecycle.Create(ctx, another); !errors.Is(err, project.ErrSlugTaken) {
		t.Fatalf("purged address reassigned: %v", err)
	}
	if _, err := f.lifecycle.projects.BySlug(ctx, in.Slug); err == nil {
		t.Fatal("purged reservation resolved to a project")
	}
}
