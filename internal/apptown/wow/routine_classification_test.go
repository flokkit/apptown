package wow

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

func routineClassificationFixture(t *testing.T) (*database.DB, *identity.Service, RoutineTransitions, context.Context, RoutinePublicationConsent, ids.ID) {
	t.Helper()
	db := testDB(t)
	people := identity.New(db, identity.Config{})
	account, err := people.EnsurePerson(t.Context(), "standards-manager")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := people.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: account.Username, Role: access.RoleStandardsAdmin, Scope: access.ScopeInstance, By: account.Username}, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{ID: account.PrincipalID, Name: account.Username, Kind: "user", Via: "session"}, Verified: true})
	lib := NewLibrary(db)
	block, err := lib.Create(ctx, "team/clarity", "Use descriptive names.", "", account.Username)
	if err != nil {
		t.Fatal(err)
	}
	if err = lib.Bump(ctx, block.ID, "Use descriptive names and document boundaries.", "[]", account.Username, "clarity"); err != nil {
		t.Fatal(err)
	}
	owner := NewRoutineTransitions(db, access.Checked{Authority: people}, people, nil)
	transition := RoutinePublicationTransition{Kind: "block", ID: block.ID, From: 1, To: 2}
	question, err := owner.Preview(ctx, transition)
	if err != nil || !question.Eligible {
		t.Fatalf("preview: %+v %v", question, err)
	}
	return db, people, owner, ctx, RoutinePublicationConsent{RequestID: ids.New(time.Now()), Transition: transition, ShownDigest: question.Digest}, grant
}

func TestRoutineConsentRetryCannotReenableRevokedMeaning(t *testing.T) {
	db, _, s, ctx, in, _ := routineClassificationFixture(t)
	first, err := s.Classify(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.Classify(ctx, in)
	if err != nil || replay.ID != first.ID {
		t.Fatalf("equal retry changed consent: %+v %v", replay, err)
	}
	if _, err = s.Revoke(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	replay, err = s.Classify(ctx, in)
	if err != nil || replay.ID != first.ID || replay.Revocation == nil {
		t.Fatalf("lost-response retry revived revoked consent: %+v %v", replay, err)
	}
	history, err := s.History(ctx, in.Transition)
	if err != nil || len(history) != 1 || history[0].Revocation == nil {
		t.Fatalf("retry created new authority: %+v %v", history, err)
	}
	if got := receipts(t, db, "publication.classified-routine", first.ID.String()); len(got) != 1 {
		t.Fatalf("retry duplicated classification receipt: %+v", got)
	}
	changed := in
	changed.ShownDigest = "different"
	if _, err = s.Classify(ctx, changed); fault.KindOf(err) != fault.Conflict {
		t.Fatalf("request key accepted changed input: %v", err)
	}
	in.RequestID = ids.New(time.Now())
	next, err := s.Classify(ctx, in)
	if err != nil || next.ID == first.ID || next.Revocation != nil {
		t.Fatalf("new explicit act after revoke: %+v %v", next, err)
	}
}

func TestRoutineConsentFailureHasNoDecisionOrAudit(t *testing.T) {
	for _, statement := range []string{"-- name: InsertRoutineClassification :one", "-- name: AppendReceipt :execrows"} {
		t.Run(statement, func(t *testing.T) {
			db, _, s, ctx, in, _ := routineClassificationFixture(t)
			dbtest.FailNextStatement(t, db, statement)
			if _, err := s.Classify(ctx, in); err == nil {
				t.Fatal("failed classification was reported successful")
			}
			history, err := s.History(ctx, in.Transition)
			if err != nil || len(history) != 0 {
				t.Fatalf("failed transaction retained consent: %+v %v", history, err)
			}
			if got := receipts(t, db, "publication.classified-routine", ""); len(got) != 0 {
				t.Fatalf("failed transaction retained audit: %+v", got)
			}
			if _, err = s.Classify(ctx, in); err != nil {
				t.Fatalf("restored write failed: %v", err)
			}
		})
	}
}

func TestRoutineConsentRequiresCurrentHumanAndStandardsAuthority(t *testing.T) {
	db, people, s, ctx, in, grant := routineClassificationFixture(t)
	caller, _ := access.CallerFrom(ctx)
	for _, tc := range []struct {
		name   string
		change func(*access.Principal)
	}{
		{"run", func(p *access.Principal) { p.Instrument = "run" }},
		{"chat", func(p *access.Principal) { p.Instrument = "console"; p.ConsoleID = "chat" }},
		{"oauth", func(p *access.Principal) { p.Via = "oauth" }},
		{"agent", func(p *access.Principal) { p.Via = "token"; p.Purpose = access.TokenAgent }},
	} {
		pr := caller.Principal
		tc.change(&pr)
		if _, err := s.Classify(access.WithCaller(ctx, access.Caller{Principal: pr, Verified: true}), in); err == nil {
			t.Fatalf("instrument %s supplied human classification", tc.name)
		}
	}
	unverified := caller
	unverified.Verified = false
	if _, err := s.Classify(access.WithCaller(ctx, unverified), in); err == nil {
		t.Fatal("unverified caller classified")
	}
	r, err := s.Classify(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	check := func() error {
		return db.InTx(ctx, func(tx *database.Tx) error { _, e := s.CheckIn(ctx, tx, []ids.ID{r.ID}); return e })
	}
	if err = check(); err != nil {
		t.Fatalf("current classification refused: %v", err)
	}
	if _, err = people.Revoke(ctx, grant, caller.Principal.Name); err != nil {
		t.Fatal(err)
	}
	if err = check(); err == nil {
		t.Fatal("withdrawn standards authority remained executable")
	}
}

func TestRoutineConsentConcurrentRequestsDoNotDuplicateAuthority(t *testing.T) {
	_, _, s, ctx, in, _ := routineClassificationFixture(t)
	var wg sync.WaitGroup
	results := make(chan RoutinePublicationClassification, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { r, e := s.Classify(ctx, in); results <- r; errs <- e })
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var id ids.ID
	for r := range results {
		if id.IsZero() {
			id = r.ID
		} else if id != r.ID {
			t.Fatal("equal concurrent requests created different consent")
		}
	}
	other := in
	other.RequestID = ids.New(time.Now())
	if _, err := s.Classify(ctx, other); fault.KindOf(err) != fault.Conflict {
		t.Fatalf("another request duplicated active consent: %v", err)
	}
	history, err := s.History(ctx, in.Transition)
	if err != nil || len(history) != 1 {
		t.Fatalf("concurrent history: %+v %v", history, err)
	}
}

func TestRestoredRoutineConsentRequiresNewLocalAct(t *testing.T) {
	db, _, s, ctx, in, _ := routineClassificationFixture(t)
	r, err := s.Classify(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, db, "-- name: AppendReceipt :execrows")
	if err = DisableRestoredRoutineClassifications(ctx, db, time.Now()); err == nil {
		t.Fatal("restore ignored missing revocation audit")
	}
	if err = db.InTx(ctx, func(tx *database.Tx) error { _, e := s.CheckIn(ctx, tx, []ids.ID{r.ID}); return e }); err != nil {
		t.Fatalf("failed restore revoke did not roll back: %v", err)
	}
	for range 2 {
		if err = DisableRestoredRoutineClassifications(ctx, db, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.InTx(ctx, func(tx *database.Tx) error { _, e := s.CheckIn(ctx, tx, []ids.ID{r.ID}); return e }); err == nil {
		t.Fatal("restored consent remained usable")
	}
	if got := receipts(t, db, "publication.routine-revoked", r.ID.String()); len(got) != 1 {
		t.Fatalf("restore repeat duplicated history: %+v", got)
	}
}

func TestRoutineConsentDifferentConcurrentRequestsHaveOneWinner(t *testing.T) {
	db, _, s, ctx, in, _ := routineClassificationFixture(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		command := in
		command.RequestID = ids.New(time.Now())
		go func() { <-start; _, err := s.Classify(ctx, command); results <- err }()
	}
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case fault.KindOf(err) == fault.Conflict:
			conflicts++
		default:
			t.Fatalf("unexpected classification failure: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("different concurrent acts: success=%d conflict=%d", successes, conflicts)
	}
	history, err := s.History(ctx, in.Transition)
	if err != nil || len(history) != 1 {
		t.Fatalf("concurrent authority: %+v %v", history, err)
	}
	if got := receipts(t, db, "publication.classified-routine", ""); len(got) != 1 {
		t.Fatalf("concurrent receipts: %+v", got)
	}
}

func TestRoutineConsentClaimLockOrdersRevocation(t *testing.T) {
	db, _, s, ctx, in, _ := routineClassificationFixture(t)
	r, err := s.Classify(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = s.CheckIn(ctx, tx, []ids.ID{r.ID}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, e := s.Revoke(ctx, r.ID); done <- e }()
	dbtest.WaitForLock(t, db, "RevokeRoutineClassification")
	select {
	case err = <-done:
		t.Fatalf("revocation escaped held claim lock: %v", err)
	default:
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = db.InTx(ctx, func(tx *database.Tx) error { _, e := s.CheckIn(ctx, tx, []ids.ID{r.ID}); return e }); err == nil {
		t.Fatal("claim after committed revocation succeeded")
	}
}

func TestRoutinePackConsentRoundTripsCompletePublishedMaterial(t *testing.T) {
	db, _, s, ctx, in, _ := routineClassificationFixture(t)
	if _, _, err := NewWorkflows(db).Put(ctx, fmt.Sprintf(laneYAML, "team-flow", "Unchanged approval", ""), "standards-manager"); err != nil {
		t.Fatal(err)
	}
	if err := NewPolicies(db).SaveLibrary(ctx, LibraryPolicy{Name: "team-policy", Rule: "requireCapabilityBound", With: map[string]any{"capability": "read"}}, "standards-manager"); err != nil {
		t.Fatal(err)
	}
	packs := NewPacks(db)
	if _, err := packs.Publish(ctx, "base", "Base", `{"guidance":["team/clarity"],"workflows":["team-flow"],"policies":[{"name":"team-policy"}]}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := NewLibrary(db).Bump(ctx, in.Transition.ID, "Use descriptive names, document boundaries, and explain intent.", "[]", "standards-manager", "clarity"); err != nil {
		t.Fatal(err)
	}
	if _, err := packs.CutOnPinChange(ctx); err != nil {
		t.Fatal(err)
	}
	if err := packs.Recommend(ctx, "base", 2, "standards-manager", ids.Nil); err != nil {
		t.Fatal(err)
	}
	pack, err := packs.ByName(ctx, "base")
	if err != nil {
		t.Fatal(err)
	}
	transition := RoutinePublicationTransition{Kind: "pack", ID: pack.ID, From: 1, To: 2}
	question, err := s.Preview(ctx, transition)
	if err != nil || !question.Eligible {
		t.Fatalf("published pack guidance transition: %+v %v", question, err)
	}
	r, err := s.Classify(ctx, RoutinePublicationConsent{RequestID: ids.New(time.Now()), Transition: transition, ShownDigest: question.Digest})
	if err != nil {
		t.Fatal(err)
	}
	history, err := s.History(ctx, transition)
	if err != nil || len(history) != 1 || history[0].Question.Digest != question.Digest {
		t.Fatalf("JSONB roundtrip: %+v %v", history, err)
	}
	if err = db.InTx(ctx, func(tx *database.Tx) error { _, e := s.CheckIn(ctx, tx, []ids.ID{r.ID}); return e }); err != nil {
		t.Fatalf("published pack could not be checked after persistence: %v", err)
	}
	covered := []RoutinePublicationTransition{transition, {Kind: "block", ID: in.Transition.ID, From: 2, To: 3}}
	if err = db.InTx(ctx, func(tx *database.Tx) error {
		records, e := s.CheckTransitionsIn(ctx, tx, covered)
		if e == nil && (len(records) != 1 || records[0].ID != r.ID) {
			t.Fatal("pack coverage changed consent identity")
		}
		return e
	}); err != nil {
		t.Fatalf("exact nested interval not covered: %v", err)
	}
	covered[1].From = 1
	if err = db.InTx(ctx, func(tx *database.Tx) error { _, e := s.CheckTransitionsIn(ctx, tx, covered); return e }); err == nil {
		t.Fatal("pack receipt covered a different nested interval")
	}
	original, err := routinePublicationIn(ctx, db, "block", in.Transition.ID, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"body", "source", "signer"} {
		t.Run(field, func(t *testing.T) {
			changed := original
			switch field {
			case "body":
				changed.Body = "Substituted historical guidance"
			case "source":
				changed.Source = "foreign-publication"
			case "signer":
				changed.Signer = "foreign-signer"
			}
			// sql-guarantee: corrupt an exact historical publication; supported owner operations only append versions and cannot express this substitution.
			if _, err := db.SQL().ExecContext(context.Background(), `UPDATE guidance_block_versions SET body_md=$1,source=$2,signer=$3 WHERE block_id=$4 AND version=$5`, changed.Body, changed.Source, changed.Signer, in.Transition.ID, 3); err != nil {
				t.Fatal(err)
			}
			if err := db.InTx(ctx, func(tx *database.Tx) error { _, e := s.CheckIn(ctx, tx, []ids.ID{r.ID}); return e }); err == nil {
				t.Fatal("captured nested publication material changed without refusal")
			}
		})
	}
}

func TestRoutineConsentDoesNotOutliveItsEnabledIssuerIdentity(t *testing.T) {
	db, people, s, ctx, in, _ := routineClassificationFixture(t)
	record, err := s.Classify(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err = people.SeedAdministrator(t.Context(), "boss"); err != nil {
		t.Fatal(err)
	}
	boss, err := people.EnsurePerson(t.Context(), "boss")
	if err != nil {
		t.Fatal(err)
	}
	principal := access.Principal{ID: boss.PrincipalID, Name: boss.Username, Kind: "user", Via: "session"}
	manager := access.WithCaller(t.Context(), access.Caller{Principal: principal, Verified: true})
	check := func() error {
		return db.InTx(ctx, func(tx *database.Tx) error { _, e := s.CheckIn(ctx, tx, []ids.ID{record.ID}); return e })
	}
	disabled := true
	if err = people.SaveUser(manager, identity.AccountChange{Username: "standards-manager", Disabled: &disabled}, principal); err != nil {
		t.Fatal(err)
	}
	if err = check(); err == nil {
		t.Fatal("disabled issuer's consent remained usable")
	}
	disabled = false
	if err = people.SaveUser(manager, identity.AccountChange{Username: "standards-manager", Disabled: &disabled}, principal); err != nil {
		t.Fatal(err)
	}
	if err = check(); err != nil {
		t.Fatalf("same reenabled identity lost its current grant: %v", err)
	}
	// A principal who has acted is retained by the identity owner. Exercise
	// that supported retirement contract rather than deleting historical identity.
	if err = people.RemoveName(manager, "standards-manager", principal); err == nil {
		t.Fatal("identity owner deleted a consent issuer with retained history")
	}
	retained, err := people.EnsurePerson(manager, "standards-manager")
	if err != nil || retained.PrincipalID != record.Issuer.Principal {
		t.Fatalf("retained issuer identity changed: %+v %v", retained, err)
	}
	disabled = true
	if err = people.SaveUser(manager, identity.AccountChange{Username: "standards-manager", Disabled: &disabled}, principal); err != nil {
		t.Fatal(err)
	}
	if err = check(); err == nil {
		t.Fatal("retired issuer's consent remained usable")
	}
}
