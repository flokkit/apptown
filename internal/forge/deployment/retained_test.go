package deployment

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func retainedFixture(t *testing.T) (*Service, *configurationWorld, SettleRetained) {
	t.Helper()
	s, w, _, dep := removalFixture(t)
	s.authority = w
	settleRemoval(t, s, dep, w.actor.Name, deploy.TakenDown{})
	rows, err := s.Retained.ForProject(context.Background(), dep.ProjectID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("retained fixture: %+v %v", rows, err)
	}
	return s, w, SettleRetained{ProjectID: dep.ProjectID, ResourceID: rows[0].ID, Disposition: RetainedReleased, Note: "deleted at provider"}
}

func assertRetainedOpen(t *testing.T, s *Service, projectID ids.ID) {
	t.Helper()
	rows, err := s.Retained.ForProject(context.Background(), projectID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("retained: %+v %v", rows, err)
	}
	r := rows[0]
	if r.Disposition != RetainedOpen || r.Note != "" || r.SettledBy != "" || r.SettledAt.Valid {
		t.Fatalf("failed settlement changed record: %+v", r)
	}
	wantN(t, "settlement receipts", receipts(t, s.db, "retained.settled"), 0)
}

func TestSettleRetainedRefusesWithoutChangingRecord(t *testing.T) {
	for _, scenario := range []string{"denied", "missing caller", "unconfigured", "invalid", "still open", "missing", "wrong project", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s, w, in := retainedFixture(t)
			projectID := in.ProjectID
			ctx := context.Background()
			var want error
			switch scenario {
			case "denied":
				want = errors.New("denied")
				w.denial = want
				in.Disposition = 0 // Authority precedes transition validation.
			case "missing caller":
				w.actor.Name = ""
			case "unconfigured":
				s.authority = nil
			case "invalid":
				in.Disposition = 0
				want = ErrRetentionState
			case "still open":
				in.Disposition = RetainedOpen
				want = ErrRetentionState
			case "missing":
				in.ResourceID = idstest.N(999999)
				want = ErrRetainedMissing
			case "wrong project":
				in.ProjectID = idstest.N(999999)
				want = ErrRetainedMissing
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			}
			err := s.SettleRetained(ctx, in)
			if err == nil || (want != nil && !errors.Is(err, want)) {
				t.Fatalf("refusal: %v, want %v", err, want)
			}
			assertRetainedOpen(t, s, projectID)
		})
	}
}

func TestSettleRetainedRollsBackAndCanBeRetried(t *testing.T) {
	for _, scenario := range []string{"update error", "audit error", "update veto", "audit veto"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s, _, in := retainedFixture(t)
			switch scenario {
			case "update error":
				dbtest.FailNextStatement(t, s.db, "-- name: SettleRetainedResource")
			case "audit error":
				dbtest.FailNextStatement(t, s.db, "INSERT INTO audit_log")
			default:
				event, table := "UPDATE", "retained_resources"
				if scenario == "audit veto" {
					event, table = "INSERT", "audit_log"
				}
				mustExec(t, s.db, `CREATE FUNCTION veto_settlement() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
				mustExec(t, s.db, `CREATE TRIGGER veto_settlement BEFORE `+event+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION veto_settlement()`)
			}
			if err := s.SettleRetained(context.Background(), in); err == nil {
				t.Fatal("hidden failed write")
			}
			assertRetainedOpen(t, s, in.ProjectID)
			if scenario == "update veto" {
				mustExec(t, s.db, `DROP TRIGGER veto_settlement ON retained_resources`)
			}
			if scenario == "audit veto" {
				mustExec(t, s.db, `DROP TRIGGER veto_settlement ON audit_log`)
			}
			if err := s.SettleRetained(context.Background(), in); err != nil {
				t.Fatalf("explicit retry: %v", err)
			}
			wantN(t, "settlement receipts", receipts(t, s.db, "retained.settled"), 1)
		})
	}
}

func TestSettleRetainedHasOneWinnerAndMatchingReceipt(t *testing.T) {
	t.Parallel()
	s, _, in := retainedFixture(t)
	requests := []SettleRetained{in, in}
	requests[1].Note = "deleted by the other person"
	errs := make([]error, len(requests))
	codes := race(len(requests), func(i int) int {
		errs[i] = s.SettleRetained(context.Background(), requests[i])
		if errs[i] == nil {
			return 1
		}
		return 0
	})
	if count(codes, 1) != 1 {
		t.Fatalf("competing settlements: %v", errs)
	}
	winner := requests[0]
	for i, err := range errs {
		if err == nil {
			winner = requests[i]
		} else if !errors.Is(err, ErrRetainedMissing) {
			t.Fatal(err)
		}
	}
	rows, err := s.Retained.ForProject(context.Background(), in.ProjectID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("retained: %+v %v", rows, err)
	}
	r := rows[0]
	if r.Disposition != winner.Disposition || r.Note != winner.Note || r.SettledBy != "alice" || !r.SettledAt.Valid {
		t.Fatalf("winner was not preserved: %+v, want %+v", r, winner)
	}
	settled := receipts(t, s.db, "retained.settled")
	wantN(t, "settlement receipts", settled, 1)
	// Exact keys and string values, as payload_json->>'disposition' and ->>'note' required.
	payload := receiptPayload(t, settled[0])
	if r := settled[0]; r.Actor != "alice" || r.ProjectID != in.ProjectID || r.Subject != fmt.Sprint(in.ResourceID) || payload["disposition"] != winner.Disposition.String() || payload["note"] != winner.Note {
		t.Fatalf("settlement receipt does not match the winner: %+v %s", r, r.Payload)
	}
	if err := s.SettleRetained(context.Background(), winner); !errors.Is(err, ErrRetainedMissing) {
		t.Fatalf("repeated settlement: %v", err)
	}
}

func TestSettleRetainedCancelsBlockedUpdate(t *testing.T) {
	t.Parallel()
	s, _, in := retainedFixture(t)
	lock, err := s.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Rollback() }()
	// sql-guarantee: the test holds one retained row's lock to block settlement; the owner has no statement that only locks it.
	if err := lock.QueryRow(`SELECT id FROM retained_resources WHERE id=? FOR UPDATE`, in.ResourceID).Scan(new(ids.ID)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.SettleRetained(ctx, in) }()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	dbtest.WaitForLock(t, s.db, "-- name: SettleRetainedResource")
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled update succeeded")
		}
	case <-deadline.C:
		t.Fatal("cancellation did not release blocked command")
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertRetainedOpen(t, s, in.ProjectID)
}
