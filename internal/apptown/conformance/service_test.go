package conformance

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type testActorKey struct{}
type testAuthority struct{ check func(permission) error }

func (a testAuthority) Actor(ctx context.Context) (string, error) {
	who, _ := ctx.Value(testActorKey{}).(string)
	if who == "" {
		return "", ErrCallerRequired
	}
	return who, nil
}
func (a testAuthority) Require(ctx context.Context, p permission) (string, error) {
	who, err := a.Actor(ctx)
	if err != nil {
		return "", err
	}
	if a.check != nil {
		if err := a.check(p); err != nil {
			return "", err
		}
	}
	return who, nil
}
func caller(who string) context.Context {
	return context.WithValue(context.Background(), testActorKey{}, who)
}

type testSource struct {
	testCheckSource
	head  string
	calls int
}

func (s *testSource) Head(_ context.Context, p ProjectSource) (SourceHead, error) {
	s.calls++
	return SourceHead{SHA: s.head}, nil
}

func TestServiceRequiresCallerBeforeReadingOrWriting(t *testing.T) {
	t.Parallel()
	// A nil database makes any attempted persistence access fail this test.
	service := newServiceWithAuthority(nil, testAuthority{}, nil)
	ctx := context.Background()
	operations := []func() error{
		func() error { _, e := service.BeginReview(ctx, ReviewRequest{}); return e },
		func() error { _, e := service.Reviews(ctx, 10); return e },
		func() error { _, e := service.Review(ctx, idstest.N(1)); return e },
		func() error { _, e := service.View(ctx, ids.Nil); return e },
		func() error { _, e := service.ProjectHistory(ctx, idstest.N(1), 10); return e },
		func() error { _, e := service.ProjectFixes(ctx, idstest.N(1), 10); return e },
		func() error { _, e := service.LatestStanding(ctx, idstest.N(1), "p"); return e },
		func() error { _, e := service.Dispositions(ctx, idstest.N(1), true); return e },
		func() error { _, e := service.Disposition(ctx, idstest.N(1)); return e },
		func() error { _, e := service.AdoptedChecks(ctx, idstest.N(1)); return e },
		func() error { _, e := service.Confirm(ctx, Confirmation{VerdictID: idstest.N(1)}); return e },
		func() error {
			_, e := service.CreateDisposition(ctx, ExceptionRequest{ProjectID: idstest.N(1)})
			return e
		},
		func() error { _, e := service.RevokeDisposition(ctx, idstest.N(1)); return e },
	}
	for i, operation := range operations {
		if err := operation(); !errors.Is(err, ErrCallerRequired) {
			t.Fatalf("operation %d: %v", i, err)
		}
	}
}

func TestReviewExecutionOwnsItsIdentityAndAdmittedInputs(t *testing.T) {
	t.Parallel()
	db, id := reviewFixture(t)
	source := &testSource{head: "committed-before-evaluation"}
	service := newServiceWithAuthority(db, testAuthority{check: func(p permission) error {
		if p.Action != runReview || len(p.Projects) != 1 || p.Projects[0] != id {
			return errScope
		}
		p.Projects[0] = idstest.N(999999)
		return nil
	}}, source)
	selected := []ids.ID{id}
	execution, err := service.BeginReview(caller("verified-person"), ReviewRequest{Projects: selected})
	if err != nil {
		t.Fatal(err)
	}
	selected[0] = idstest.N(999999)
	projects, err := execution.Projects(caller("verified-person"))
	if err != nil || len(projects) != 1 {
		t.Fatalf("plan: %+v %v", projects, err)
	}
	declared := projects[0].Checks[0]
	projects[0].Checks[0].Check.Name = "apptownd"
	projects[0].Slug = "apptownd"
	assessment := Assessment{ProjectID: id, Check: CheckIdentity{Block: declared.Block, Version: declared.Version, Check: declared.Check.Name}, State: Conforms}
	pending, err := execution.PrepareVerdict(context.Background(), assessment)
	if err != nil {
		t.Fatal(err)
	}
	source.head = "moved-after-evaluation"
	head, err := execution.Source(context.Background(), id)
	if err != nil || head.SHA != "committed-before-evaluation" || source.calls != 1 {
		t.Fatalf("source changed: %+v %v calls=%d", head, err, source.calls)
	}
	for _, bad := range []Assessment{{ProjectID: idstest.N(999999), Check: assessment.Check, State: Conforms}, {ProjectID: id, Check: CheckIdentity{Block: declared.Block, Version: declared.Version, Check: "apptownd"}, State: Conforms}, {ProjectID: id, Check: assessment.Check, State: 0}} {
		if p, e := execution.PrepareVerdict(context.Background(), bad); e == nil || p != nil {
			t.Fatalf("invalid assessment accepted: %+v %v", p, e)
		}
	}
	saved, err := pending.Commit(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if saved.SHA != head.SHA || saved.ProjectSlug != "reviewed" || saved.Check != "clean" || saved.ReviewID != execution.Review().ID || execution.Review().Actor != "verified-person" {
		t.Fatalf("unbound identity: %+v %+v", saved, execution.Review())
	}
	if _, err := pending.Commit(context.Background(), nil); !errors.Is(err, errVerdictChanged) {
		t.Fatalf("duplicate admission: %v", err)
	}
	if err := execution.Progress(context.Background(), 1, "reviewed"); err != nil {
		t.Fatal(err)
	}
	finished, err := execution.Finish(context.Background(), Completion{Projects: 1, ProjectsDone: 1}, ReviewDone)
	if err != nil || finished.Verdicts != 1 {
		t.Fatalf("finish: %+v %v", finished, err)
	}
	if _, err := pending.Commit(context.Background(), nil); !errors.Is(err, errReviewClosed) {
		t.Fatalf("terminal write: %v", err)
	}
	wantHeld(t, "verdicts", heldRecords(t, db).Verdicts, 1)
}

func TestExecutionExceptionsAreOneDetachedHistoricalSnapshot(t *testing.T) {
	t.Parallel()
	db, id := reviewFixture(t)
	service := newServiceWithAuthority(db, testAuthority{}, &testSource{head: "head"})
	execution, err := service.BeginReview(caller("reviewer"), ReviewRequest{Projects: []ids.ID{id}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := execution.Exceptions(context.Background(), id)
	if err != nil || len(first) != 1 {
		t.Fatalf("snapshot: %+v %v", first, err)
	}
	savedID := first[0].ID
	first[0].Justification = "caller edited"
	if _, err := service.RevokeDisposition(caller("revoker"), savedID); err != nil {
		t.Fatal(err)
	}
	again, err := execution.Exceptions(context.Background(), id)
	if err != nil || len(again) != 1 || again[0].ID != savedID || again[0].Justification == "caller edited" {
		t.Fatalf("history changed: %+v %v", again, err)
	}
	wantHeld(t, "review exceptions", heldRecords(t, db).Exceptions, 1)
	if n := projectDispositions(t, db, id); n != 0 {
		t.Fatalf("dispositions: %d", n)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := execution.Exceptions(canceled, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("cached snapshot ignored cancellation: %v", err)
	}
}

func TestReviewCannotInventMissingCommittedSource(t *testing.T) {
	t.Parallel()
	db, id := reviewFixture(t)
	service := newServiceWithAuthority(db, testAuthority{}, &testSource{})
	execution, err := service.BeginReview(caller("reviewer"), ReviewRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if head, err := execution.Source(context.Background(), id); !errors.Is(err, ErrSource) || head.SHA != "" {
		t.Fatalf("missing source: %+v %v", head, err)
	}
	if r, err := execution.Finish(context.Background(), Completion{Errors: 1}, ReviewError); err != nil || r.Status != ReviewError {
		t.Fatalf("failure: %+v %v", r, err)
	}
	wantHeld(t, "verdicts", heldRecords(t, db).Verdicts, 0)
}

func TestExceptionCommandsRequireTheirAuditToCommit(t *testing.T) {
	t.Parallel()
	for _, deferred := range []bool{false, true} {
		t.Run(fmt.Sprint(deferred), func(t *testing.T) {
			t.Parallel()
			db, id := reviewFixture(t)
			service := newServiceWithAuthority(db, testAuthority{}, nil)
			request := ExceptionRequest{ProjectID: id, Identifier: "CVE-fixture", Kind: AcceptedRisk, Justification: "acknowledged"}
			drop := vetoConformanceReceipt(t, db, "audit_log", deferred)
			if saved, err := service.CreateDisposition(caller("verified-person"), request); err == nil || saved != nil {
				t.Fatalf("missing create audit: %+v %v", saved, err)
			}
			if n := projectDispositions(t, db, id); n != 1 {
				t.Fatalf("dispositions: %d", n)
			}
			drop()
			saved, err := service.CreateDisposition(caller("verified-person"), request)
			if err != nil || saved.By != "verified-person" {
				t.Fatalf("create: %+v %v", saved, err)
			}
			// Reuse the veto function, replacing its removed trigger.
			clause := "BEFORE INSERT"
			kind := "CREATE TRIGGER"
			deferSQL := ""
			if deferred {
				clause = "AFTER INSERT"
				kind = "CREATE CONSTRAINT TRIGGER"
				deferSQL = " DEFERRABLE INITIALLY DEFERRED"
			}
			completionExec(t, db, kind+` reject_receipt `+clause+` ON audit_log`+deferSQL+` FOR EACH ROW EXECUTE FUNCTION reject_receipt()`)
			if removed, err := service.RevokeDisposition(caller("revoker"), saved.ID); err == nil || removed != nil {
				t.Fatalf("missing revoke audit: %+v %v", removed, err)
			}
			if n := projectDispositions(t, db, id); n != 2 {
				t.Fatalf("dispositions: %d", n)
			}
			wantReceipts(t, db, "disposition.revoked", 0)
			drop()
			removed, err := service.RevokeDisposition(caller("revoker"), saved.ID)
			if err != nil || removed.By != "revoker" {
				t.Fatalf("revoke: %+v %v", removed, err)
			}
		})
	}
}

func TestReviewExceptionSnapshotRejectsSuppressedRows(t *testing.T) {
	t.Parallel()
	db, id := reviewFixture(t)
	service := newServiceWithAuthority(db, testAuthority{}, unavailableHeadSource{})
	execution, err := service.BeginReview(caller("reviewer"), ReviewRequest{})
	if err != nil {
		t.Fatal(err)
	}
	vetoConformanceReceipt(t, db, "conformance_review_exceptions", false)
	if saved, err := execution.Exceptions(context.Background(), id); err == nil || saved != nil {
		t.Fatalf("unrecorded snapshot: %+v %v", saved, err)
	}
	wantHeld(t, "review exceptions", heldRecords(t, db).Exceptions, 0)
}

func newServiceWithAuthority(db *database.DB, authority operationAuthority, source Source) *Service {
	s := New(db, nil, source, nil)
	s.authority = authority
	return s
}
