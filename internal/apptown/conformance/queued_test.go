package conformance

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

var errRollback = errors.New("the caller's transaction rolls back")

// A queued review keeps what was asked, so the job naming only its ID runs
// exactly that, and only the first claim runs it. Kills: a selection or
// build choice lost between admission and claim (the whole estate read for
// none, or none for the whole estate); a second delivery running it again.
func TestAQueuedReviewIsClaimedOnceWithItsSavedRequest(t *testing.T) {
	t.Parallel()
	db, project := reviewFixture(t)
	service := newServiceWithAuthority(db, testAuthority{}, unavailableHeadSource{testCheckSource{}})
	ctx := caller("reviewer")
	for _, in := range []ReviewRequest{
		{Trigger: "manual", Build: true, BuildBudget: 3},
		{Trigger: "manual", Projects: []ids.ID{}},
		{Trigger: "manual", Projects: []ids.ID{project}},
	} {
		rev, err := database.Transact(ctx, db, func(tx *database.Tx) (*Review, error) { return service.AdmitReviewIn(ctx, tx, in) })
		if err != nil || rev.Status != ReviewQueued || rev.Actor != "reviewer" {
			t.Fatalf("admitted %+v: %+v %v", in, rev, err)
		}
		execution, err := service.ClaimReview(context.Background(), rev.ID)
		if err != nil {
			t.Fatal(err)
		}
		got := execution.Options()
		if (got.Projects == nil) != (in.Projects == nil) || !slices.Equal(got.Projects, in.Projects) || got.Build != in.Build || got.BuildBudget != in.BuildBudget {
			t.Fatalf("asked %+v, claimed %+v", in, got)
		}
		if execution.Review().Status != ReviewRunning {
			t.Fatalf("claimed review: %+v", execution.Review())
		}
		if _, err := service.ClaimReview(context.Background(), rev.ID); !errors.Is(err, ErrNotQueued) {
			t.Fatalf("second claim: %v", err)
		}
	}
	// Admission inside a transaction that rolls back leaves no review for
	// a job to find.
	var admitted ids.ID
	_, err := database.Transact(ctx, db, func(tx *database.Tx) (*Review, error) {
		rev, err := service.AdmitReviewIn(ctx, tx, ReviewRequest{Trigger: "manual"})
		if err != nil {
			return nil, err
		}
		admitted = rev.ID
		return nil, errRollback
	})
	if !errors.Is(err, errRollback) || admitted.IsZero() {
		t.Fatalf("admission: %d %v", admitted, err)
	}
	if _, err := service.Review(ctx, admitted); !errors.Is(err, ErrNoReview) {
		t.Fatalf("a rolled-back admission left its review: %v", err)
	}
}

// A queued what-if keeps its request and is claimed once; stopped by
// shutdown or its time limit it ends interrupted, any other failure an
// error. Kills: a lost selection; a second delivery running it again; an
// interruption recorded as an error.
func TestAQueuedWhatIfIsClaimedOnceAndEndsInterruptedWhenStopped(t *testing.T) {
	t.Parallel()
	db, project := reviewFixture(t)
	service := newServiceWithAuthority(db, testAuthority{}, unavailableHeadSource{testCheckSource{}})
	ctx := caller("reviewer")
	for _, end := range []struct {
		err   error
		state string
	}{
		{fmt.Errorf("%w: %w", ErrInterrupted, context.Canceled), WhatIfInterrupted},
		{errors.New("source unavailable"), WhatIfError},
	} {
		in := WhatIfRequest{Pack: "team", Version: 2, Projects: []ids.ID{project}, Rubrics: true, SpendLimitMicros: 500}
		id, err := database.Transact(ctx, db, func(tx *database.Tx) (ids.ID, error) { return service.AdmitWhatIfIn(ctx, tx, in, nil) })
		if err != nil {
			t.Fatal(err)
		}
		if w, err := service.WhatIf(ctx, id); err != nil || w.State != WhatIfQueued || len(w.Projects) != 1 || w.Projects[0].Outcome != WhatIfNotYetJudged {
			t.Fatalf("admitted: a queued what-if's project is not yet judged, not ended: %+v %v", w, err)
		}
		run, got, err := service.ClaimWhatIf(context.Background(), id)
		if err != nil || got.Pack != in.Pack || got.Version != in.Version || !slices.Equal(got.Projects, in.Projects) || !got.Rubrics || got.SpendLimitMicros != 500 {
			t.Fatalf("asked %+v, claimed %+v %v", in, got, err)
		}
		if w, err := service.WhatIf(ctx, id); err != nil || w.State != WhatIfRunning || w.Projects[0].Outcome != WhatIfNotYetJudged {
			t.Fatalf("a running what-if's unreached project is not yet judged: %+v %v", w, err)
		}
		if _, _, err := service.ClaimWhatIf(context.Background(), id); !errors.Is(err, ErrNotQueued) {
			t.Fatalf("second claim: %v", err)
		}
		if err := run.Finish(context.Background(), end.err); err != nil {
			t.Fatal(err)
		}
		if w, err := service.WhatIf(ctx, id); err != nil || w.State != end.state || !w.FinishedAt.Valid || w.Projects[0].Outcome != WhatIfNotEvaluated {
			t.Fatalf("ended by %v: its unreached project is not evaluated: %+v %v", end.err, w, err)
		}
	}
	// No projects named (nil) is the eligible ones, stored as the list they
	// were; an empty list stays empty.
	for _, c := range []struct {
		selection, want []ids.ID
	}{{nil, []ids.ID{project}}, {[]ids.ID{}, []ids.ID{}}} {
		id, err := database.Transact(ctx, db, func(tx *database.Tx) (ids.ID, error) {
			return service.AdmitWhatIfIn(ctx, tx, WhatIfRequest{Pack: "team", Version: 2, Projects: c.selection}, func() ([]ids.ID, error) { return []ids.ID{project}, nil })
		})
		if err != nil {
			t.Fatal(err)
		}
		_, got, err := service.ClaimWhatIf(context.Background(), id)
		if err != nil || got.Projects == nil || !slices.Equal(got.Projects, c.want) {
			t.Fatalf("selection %#v claimed as %#v: %v", c.selection, got.Projects, err)
		}
	}
}

// A what-if that names no projects reads the eligible ones only once the
// starter's authority holds, and with none eligible it is refused and
// nothing is admitted. Kills: eligibility read before authority (it would
// tell a refused caller which projects hold the pack); an empty eligible
// set admitted as a what-if over nothing, or as the whole estate.
func TestAWhatIfOnTheEligibleProjectsAsksAuthorityFirst(t *testing.T) {
	t.Parallel()
	db, _ := reviewFixture(t)
	denied := errors.New("denied")
	refusing := newServiceWithAuthority(db, testAuthority{check: func(Permission) error { return denied }}, unavailableHeadSource{testCheckSource{}})
	ctx := caller("reviewer")
	read := false
	_, err := database.Transact(ctx, db, func(tx *database.Tx) (ids.ID, error) {
		return refusing.AdmitWhatIfIn(ctx, tx, WhatIfRequest{Pack: "team", Version: 2}, func() ([]ids.ID, error) { read = true; return nil, nil })
	})
	if !errors.Is(err, denied) || read {
		t.Fatalf("a refused starter: %v, eligibility read %v", err, read)
	}
	service := newServiceWithAuthority(db, testAuthority{}, unavailableHeadSource{testCheckSource{}})
	_, err = database.Transact(ctx, db, func(tx *database.Tx) (ids.ID, error) {
		return service.AdmitWhatIfIn(ctx, tx, WhatIfRequest{Pack: "team", Version: 2}, func() ([]ids.ID, error) { return []ids.ID{}, nil })
	})
	if fault.KindOf(err) != fault.Invalid {
		t.Fatalf("no eligible project: %v", err)
	}
	if w, err := service.LatestWhatIf(ctx, "team", 2); err != nil || w != nil {
		t.Fatalf("a refused what-if was admitted: %+v %v", w, err)
	}
}
