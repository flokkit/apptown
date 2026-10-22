package conformance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/conformance/internal/conformancesql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/stamp"

	"reflect"

	"testing"
)

func verdictReceiptFixture(t *testing.T, evaluator string) (reviewStore, Verdict, verdictAdmission) {
	t.Helper()
	return verdictReceiptFixtureWithOutcome(t, evaluator, DoesNotConform)
}

func verdictReceiptFixtureWithOutcome(t *testing.T, evaluator string, state Outcome) (reviewStore, Verdict, verdictAdmission) {
	t.Helper()
	db, id := reviewFixture(t)
	s := reviewStore{db: db, checks: testCheckSource{}}
	first, err := s.create(context.Background(), s.db, ReviewRequest{Trigger: "manual"}, "reviewer", ReviewRunning)
	if err != nil {
		t.Fatal(err)
	}
	prev, err := s.recordVerdict(context.Background(), verdictAdmission{Verdict: Verdict{ReviewID: first.ID, ProjectID: id, ProjectSlug: "reviewed", SHA: "before", Block: "hygiene", Version: 1, Check: "clean", Evaluator: evaluator, Mode: "posture", State: state, At: stamp.Of(time.Now()).String()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.finish(context.Background(), *first, ReviewDone); err != nil {
		t.Fatal(err)
	}
	second, err := s.create(context.Background(), s.db, ReviewRequest{Trigger: "manual"}, "reviewer", ReviewRunning)
	if err != nil {
		t.Fatal(err)
	}
	next := prev
	next.ID, next.ReviewID, next.SHA, next.State = ids.Nil, second.ID, "after", Conforms
	req := verdictAdmission{Verdict: next, PreviousID: prev.ID}
	if evaluator != "llm" {
		req.Fix = &FixAttribution{Changes: []ids.ID{idstest.N(7)}, AcceptedBy: access.Actor{Principal: idstest.N(9), Name: "reviewer", Via: "session"}}
	}
	return s, prev, req
}

func vetoConformanceReceipt(t *testing.T, db *database.DB, table string, deferred bool) func() {
	t.Helper()
	body, trigger := "RETURN NULL;", "CREATE TRIGGER reject_receipt BEFORE INSERT ON "+table
	if deferred {
		body = "RAISE EXCEPTION 'commit rejected';"
		trigger = "CREATE CONSTRAINT TRIGGER reject_receipt AFTER INSERT ON " + table + " DEFERRABLE INITIALLY DEFERRED"
	}
	completionExec(t, db, `CREATE FUNCTION reject_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$`)
	completionExec(t, db, trigger+` FOR EACH ROW EXECUTE FUNCTION reject_receipt()`)
	return func() { completionExec(t, db, `DROP TRIGGER reject_receipt ON `+table) }
}

func TestConfirmationAndAuditCommitTogether(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		table    string
		deferred bool
	}{{"conformance_adjudications", false}, {"audit_log", false}, {"audit_log", true}} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			t.Parallel()
			s, prev, _ := verdictReceiptFixture(t, "llm")
			drop := vetoConformanceReceipt(t, s.db, tc.table, tc.deferred)
			req := confirmationRequest{VerdictID: prev.ID, By: "alice", Note: "  checked the evidence  "}
			if got, err := s.confirm(context.Background(), req); err == nil || got != nil {
				t.Fatalf("missing receipt accepted: %+v %v", got, err)
			}
			wantHeld(t, "adjudications", heldRecords(t, s.db).Adjudications, 0)
			wantReceipts(t, s.db, "conformance.confirmed", 0)
			drop()
			got, err := s.confirm(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			records, err := readAdjudications(context.Background(), s.db, []ids.ID{prev.ID})
			if err != nil || !reflect.DeepEqual(records[prev.ID], *got) || got.Note != "checked the evidence" {
				t.Fatalf("receipt differs from saved adjudication: %+v %+v %v", got, records, err)
			}
			wantReceipts(t, s.db, "conformance.confirmed", 1)
		})
	}
}

func TestConcurrentConfirmationHasOneWinner(t *testing.T) {
	t.Parallel()
	s, prev, _ := verdictReceiptFixture(t, "llm")
	start := make(chan struct{})
	results := make(chan error, 12)
	for i := range 12 {
		go func() {
			<-start
			_, err := s.confirm(context.Background(), confirmationRequest{VerdictID: prev.ID, By: fmt.Sprintf("reviewer-%d", i)})
			results <- err
		}()
	}
	close(start)
	winners := 0
	for range 12 {
		err := <-results
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrAlreadyConfirmed) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("confirmation winners: %d", winners)
	}
	if adjudications, err := queries(s.db).AdjudicationsForVerdicts(context.Background(), []ids.ID{prev.ID}); err != nil || len(adjudications) != 1 {
		t.Fatalf("adjudications: %d %v", len(adjudications), err)
	}
	wantReceipts(t, s.db, "conformance.confirmed", 1)
}

func TestVerdictFixAndAuditCommitTogether(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		table    string
		deferred bool
	}{{"conformance_verdicts", false}, {"conformance_fixes", false}, {"audit_log", false}, {"audit_log", true}} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			t.Parallel()
			s, prev, req := verdictReceiptFixture(t, "sh")
			drop := vetoConformanceReceipt(t, s.db, tc.table, tc.deferred)
			if got, err := s.recordVerdict(context.Background(), req); err == nil || !got.ID.IsZero() {
				t.Fatalf("uncommitted verdict/fix escaped: %+v %v", got, err)
			}
			wantHeld(t, "verdicts", heldRecords(t, s.db).Verdicts, 1)
			wantHeld(t, "fixes", heldRecords(t, s.db).Fixes, 0)
			wantReceipts(t, s.db, "conformance.fixed", 0)
			drop()
			got, err := s.recordVerdict(context.Background(), req)
			if err != nil || got.ID.IsZero() {
				t.Fatalf("retry: %+v %v", got, err)
			}
			// Re-review appends evidence: old identity/source/outcome survive exactly.
			wantHeld(t, "verdicts", heldRecords(t, s.db).Verdicts, 2)
			for _, want := range []Verdict{prev, {ID: got.ID, ReviewID: req.Verdict.ReviewID, SHA: req.Verdict.SHA, State: req.Verdict.State}} {
				stored, err := queries(s.db).VerdictByID(context.Background(), want.ID)
				if err != nil || stored.ReviewID != want.ReviewID || stored.Sha != want.SHA || stored.State != want.State.String() {
					t.Fatalf("verdict %d changed: %+v %v", want.ID, stored, err)
				}
			}
			fixes, err := queries(s.db).FixesForProject(context.Background(), conformancesql.FixesForProjectParams{ProjectID: prev.ProjectID, Limit: 10})
			if err != nil || len(fixes) != 1 || fixes[0].ConformanceFix.BeforeVerdictID != prev.ID || fixes[0].ConformanceFix.AfterVerdictID != got.ID {
				t.Fatalf("fix links: %+v %v", fixes, err)
			}
			wantHeld(t, "fixes", heldRecords(t, s.db).Fixes, 1) // the project's one fix is the only fix
			wantReceipts(t, s.db, "conformance.fixed", 1)
		})
	}
}

func TestVerdictAdmissionRejectsStaleFixPrerequisites(t *testing.T) {
	t.Parallel()
	t.Run("another review", func(t *testing.T) {
		t.Parallel()
		s, _, req := verdictReceiptFixture(t, "sh")
		other, err := s.create(context.Background(), s.db, ReviewRequest{Trigger: "manual"}, "other", ReviewRunning)
		if err != nil {
			t.Fatal(err)
		}
		competing := req
		competing.Verdict.ReviewID = other.ID
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, candidate := range []verdictAdmission{req, competing} {
			go func() { <-start; _, err := s.recordVerdict(context.Background(), candidate); results <- err }()
		}
		close(start)
		a, b := <-results, <-results
		if !((a == nil && errors.Is(b, ErrVerdictChanged)) || (b == nil && errors.Is(a, ErrVerdictChanged))) {
			t.Fatalf("expected a winner and a conflict: %v %v", a, b)
		}
		wantHeld(t, "verdicts", heldRecords(t, s.db).Verdicts, 2)
		wantHeld(t, "fixes", heldRecords(t, s.db).Fixes, 1)
	})
	t.Run("confirmation changed", func(t *testing.T) {
		t.Parallel()
		s, prev, req := verdictReceiptFixture(t, "llm")
		if _, err := s.confirm(context.Background(), confirmationRequest{VerdictID: prev.ID, By: "alice"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.recordVerdict(context.Background(), req); !errors.Is(err, ErrVerdictChanged) {
			t.Fatalf("newly confirmed flag needs a fix: %v", err)
		}
		wantHeld(t, "verdicts", heldRecords(t, s.db).Verdicts, 1)
		req.Fix = &FixAttribution{Commits: "before..after"}
		if _, err := s.recordVerdict(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		wantHeld(t, "fixes", heldRecords(t, s.db).Fixes, 1)
	})
}

func TestConfirmationReadFailuresAreNotAbsence(t *testing.T) {
	t.Parallel()
	s, prev, req := verdictReceiptFixture(t, "llm")
	dbtest.FailNextStatement(t, s.db, "FROM conformance_adjudications")
	if got, err := s.confirm(context.Background(), confirmationRequest{VerdictID: prev.ID, By: "alice"}); err == nil || got != nil {
		t.Fatalf("confirmation read failure: %+v %v", got, err)
	}
	dbtest.FailNextStatement(t, s.db, "FROM conformance_adjudications")
	if got, err := s.recordVerdict(context.Background(), req); err == nil || !got.ID.IsZero() {
		t.Fatalf("fix eligibility read failure: %+v %v", got, err)
	}
	dbtest.FailNextStatement(t, s.db, "FROM conformance_adjudications")
	if got, err := readAdjudications(context.Background(), s.db, []ids.ID{prev.ID}); err == nil || got != nil {
		t.Fatalf("batch read failure: %+v %v", got, err)
	}
	wantHeld(t, "adjudications", heldRecords(t, s.db).Adjudications, 0)
	wantHeld(t, "verdicts", heldRecords(t, s.db).Verdicts, 1)
}
