package adrs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/apptown/adrs/internal/adrssql"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
)

type permitFunc func(context.Context, access.Permission) (string, error)

func (f permitFunc) Require(ctx context.Context, p access.Permission) (access.Caller, error) {
	name, err := f(ctx, p)
	return access.Caller{Principal: access.Principal{Name: name}, Verified: name != ""}, err
}
func projectFixture(t *testing.T, db *database.DB, slug, name string) ids.ID {
	t.Helper()
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: name})
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func fixture(t *testing.T) (*Service, ids.ID) {
	t.Helper()
	db := testDB(t)
	id := projectFixture(t, db, "decisions", "Decisions")
	accounts := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := accounts.SeedAdministrator(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	return New(db, access.Checked{Authority: accounts, Projects: project.New(db, accounts)}, nil, nil), id
}
func create(t *testing.T, s *Service, p ids.ID, title string) *Record {
	t.Helper()
	v, err := s.Create(testCaller(), Create{ProjectID: p, Title: title, BodyMD: "original", Tags: " data , storage ,, "})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func decide(t *testing.T, s *Service, id ids.ID, act Decision) *Record {
	t.Helper()
	v, err := s.Edit(testCaller(), Edit{ID: id, Decision: act})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func snapshot(t *testing.T, db *database.DB) string {
	t.Helper()
	var out string
	// sql-guarantee: A failed command must leave every stored column of every record and receipt unchanged; the full-row image is the guarantee.
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT jsonb_build_object('records',(SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY id),'[]') FROM adrs a),'receipts',(SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY id),'[]') FROM audit_log a))::text`).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}
func concurrent(n int, f func(int) error) []error {
	start := make(chan struct{})
	out := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; out[i] = f(i) }()
	}
	close(start)
	wg.Wait()
	return out
}

func TestConcurrentNumberingHasOneOwner(t *testing.T) {
	t.Parallel()
	s, p := fixture(t)
	results := concurrent(16, func(i int) error {
		_, err := s.Create(testCaller(), Create{ProjectID: p, Title: fmt.Sprintf("Decision %d", i)})
		return err
	})
	for _, err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	records, err := s.List(testCaller(), Filter{ProjectID: p})
	if err != nil || len(records) != 16 {
		t.Fatalf("created records: %d %v", len(records), err)
	}
	numbers := make([]int, 0, len(records))
	for _, r := range records {
		numbers = append(numbers, int(r.Number))
		if r.Status != Draft {
			t.Fatal("new record is not a draft")
		}
	}
	sort.Ints(numbers)
	for i, n := range numbers {
		if n != i+1 {
			t.Fatalf("number collision or gap: %v", numbers)
		}
	}
	wantCount(t, "alice's creation receipts", receiptsBy(t, s.records.db, "adr.created", "alice"), 16)
}
func TestCreationRequiresRecordAndReceipt(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"number read", "insert", "receipt", "suppressed insert", "suppressed receipt"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			s, p := fixture(t)
			db := s.records.db
			switch failure {
			case "number read":
				dbtest.FailNextStatement(t, db, "-- name: NextADRNumber")
			case "insert":
				dbtest.FailNextStatement(t, db, "INSERT INTO adrs")
			case "receipt":
				dbtest.FailNextStatement(t, db, "INSERT INTO audit_log")
			default:
				table := "adrs"
				if failure == "suppressed receipt" {
					table = "audit_log"
				}
				completionExec(t, db, `CREATE FUNCTION veto_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
				completionExec(t, db, `CREATE TRIGGER veto_insert BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION veto_insert()`)
			}
			before := snapshot(t, db)
			if got, err := s.Create(testCaller(), Create{ProjectID: p, Title: "atomic"}); got != nil || err == nil {
				t.Fatalf("partial create: %+v %v", got, err)
			}
			if after := snapshot(t, db); after != before {
				t.Fatal("failed create committed a record or receipt")
			}
			if failure == "suppressed insert" {
				completionExec(t, db, `DROP TRIGGER veto_insert ON adrs`)
			}
			if failure == "suppressed receipt" {
				completionExec(t, db, `DROP TRIGGER veto_insert ON audit_log`)
			}
			if got := create(t, s, p, "explicit retry"); got.Number != 1 {
				t.Fatalf("failed request consumed decision number: %+v", got)
			}
		})
	}
}
func TestAcceptanceAndRetirementAreOneReceipt(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"predecessor write", "successor write", "receipt", "deferred receipt", "successor readback"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			s, p := fixture(t)
			db := s.records.db
			old := create(t, s, p, "original decision")
			decide(t, s, old.ID, Accept)
			next, err := s.ProposeSuccessor(testCaller(), Successor{ID: old.ID})
			if err != nil {
				t.Fatal(err)
			}
			before := snapshot(t, db)
			if failure == "receipt" {
				dbtest.FailNextStatement(t, db, "INSERT INTO audit_log")
			} else if failure == "deferred receipt" {
				completionExec(t, db, `CREATE FUNCTION veto_receipt_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'receipt refused at commit'; END $$`)
				completionExec(t, db, `CREATE CONSTRAINT TRIGGER veto_receipt_commit AFTER INSERT ON audit_log DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION veto_receipt_commit()`)
			} else {
				id := next.ID
				if failure == "predecessor write" {
					id = old.ID
				}
				action := "RETURN NULL;"
				if failure == "successor readback" {
					action = "NEW.status='unexpected'; RETURN NEW;"
				}
				completionExec(t, db, fmt.Sprintf(`CREATE FUNCTION veto_accept() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id='%s'::uuid THEN %s END IF; RETURN NEW; END $$`, id, action))
				completionExec(t, db, `CREATE TRIGGER veto_accept BEFORE UPDATE ON adrs FOR EACH ROW EXECUTE FUNCTION veto_accept()`)
			}
			if got, err := s.Edit(testCaller(), Edit{ID: next.ID, Decision: Accept}); got != nil || err == nil {
				t.Fatalf("partial acceptance: %+v %v", got, err)
			}
			if after := snapshot(t, db); after != before {
				t.Fatal("failed successor acceptance changed historical decision or receipt")
			}
			if failure == "deferred receipt" {
				completionExec(t, db, `DROP TRIGGER veto_receipt_commit ON audit_log`)
			} else if failure != "receipt" {
				completionExec(t, db, `DROP TRIGGER veto_accept ON adrs`)
			}
			decide(t, s, next.ID, Accept)
			detail, err := s.Get(testCaller(), old.ID)
			if err != nil || detail.ADR.Status != Superseded || detail.SupersededByID != next.ID || detail.ADR.BodyMD != "original" {
				t.Fatalf("historical decision lost: %+v %v", detail, err)
			}
		})
	}
}
func TestCompetingSuccessorsHaveOneWinnerAndCanRestore(t *testing.T) {
	t.Parallel()
	s, p := fixture(t)
	old := create(t, s, p, "original")
	decide(t, s, old.ID, Accept)
	var proposals []*Record
	for i := 0; i < 2; i++ {
		v, err := s.ProposeSuccessor(testCaller(), Successor{ID: old.ID, Title: fmt.Sprint(i)})
		if err != nil {
			t.Fatal(err)
		}
		proposals = append(proposals, v)
	}
	results := concurrent(2, func(i int) error {
		_, err := s.Edit(testCaller(), Edit{ID: proposals[i].ID, Decision: Accept})
		return err
	})
	winners := 0
	var winner *Record
	for i, err := range results {
		if err == nil {
			winners++
			winner = proposals[i]
		} else if !errors.Is(err, errConflict) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("acceptance winners: %v", results)
	}
	detail, err := s.Get(testCaller(), old.ID)
	if err != nil || detail.SupersededByID != winner.ID {
		t.Fatalf("detail did not choose the accepted successor: %+v %v", detail, err)
	}
	previous := detail.ADR
	decide(t, s, winner.ID, deprecate)
	decide(t, s, winner.ID, Accept)
	detail, err = s.Get(testCaller(), old.ID)
	if err != nil || detail.ADR != previous {
		t.Fatalf("restore changed predecessor history: %+v %v", detail, err)
	}
	if _, err := s.Edit(testCaller(), Edit{ID: old.ID, Decision: Accept}); !errors.Is(err, errTransition) {
		t.Fatalf("superseded decision reopened: %v", err)
	}
}
func TestConcurrentEditsMergeFieldsAndRespectAcceptance(t *testing.T) {
	t.Parallel()
	t.Run("independent fields", func(t *testing.T) {
		t.Parallel()
		s, p := fixture(t)
		v := create(t, s, p, "old")
		title, body := "new title", "new body"
		commands := []Edit{{ID: v.ID, Title: &title, Expected: TextFields{Title: &v.Title}}, {ID: v.ID, BodyMD: &body, Expected: TextFields{BodyMD: &v.BodyMD}}}
		for _, err := range concurrent(2, func(i int) error { _, err := s.Edit(testCaller(), commands[i]); return err }) {
			if err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.Get(testCaller(), v.ID)
		if err != nil || got.ADR.Title != title || got.ADR.BodyMD != body {
			t.Fatalf("lost concurrent edit: %+v %v", got, err)
		}
	})
	t.Run("edit and accept", func(t *testing.T) {
		t.Parallel()
		s, p := fixture(t)
		v := create(t, s, p, "old")
		body := "new body"
		results := concurrent(2, func(i int) error {
			cmd := Edit{ID: v.ID, Decision: Accept}
			if i == 1 {
				cmd = Edit{ID: v.ID, BodyMD: &body, Expected: TextFields{BodyMD: &v.BodyMD}}
			}
			_, err := s.Edit(testCaller(), cmd)
			return err
		})
		if results[0] != nil {
			t.Fatal(results[0])
		}
		expected := "original"
		if results[1] == nil {
			expected = body
		} else if !errors.Is(results[1], errImmutable) {
			t.Fatal(results[1])
		}
		got, err := s.Get(testCaller(), v.ID)
		if err != nil || got.ADR.Status != Accepted || got.ADR.BodyMD != expected {
			t.Fatalf("acceptance raced past content lock: %+v %v", got, err)
		}
		if _, err := s.Edit(testCaller(), Edit{ID: v.ID, BodyMD: &body}); !errors.Is(err, errImmutable) {
			t.Fatalf("accepted content changed: %v", err)
		}
	})
}

// An ADR keeps no versions, so a text edit names the text it started from
// and is refused when that text has been replaced since: two people editing
// one decision cannot silently overwrite each other (review
// web-readgate-2026-10-08, W-03). Fields the edit does not write may move.
func TestTextEditsAreRefusedWhenTheirStartingTextMoved(t *testing.T) {
	t.Parallel()
	s, p := fixture(t)
	v := create(t, s, p, "old")
	original := v.BodyMD
	a, b := "A's rationale", "B's rationale"
	if _, err := s.Edit(testCaller(), Edit{ID: v.ID, BodyMD: &a, Expected: TextFields{BodyMD: &original}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Edit(testCaller(), Edit{ID: v.ID, BodyMD: &b, Expected: TextFields{BodyMD: &original}}); !errors.Is(err, errTextMoved) || fault.CodeOf(err) != "adr-text-moved" {
		t.Fatalf("an edit from replaced text must be refused: %v", err)
	}
	if _, err := s.Edit(testCaller(), Edit{ID: v.ID, BodyMD: &b}); !errors.Is(err, errTextExpected) {
		t.Fatalf("a text edit naming no starting text must be refused: %v", err)
	}
	// Tags are compared as stored (normalised), and a tags edit from the
	// current tags succeeds although the body moved.
	tags, startTags := "data", " data , storage ,, "
	if _, err := s.Edit(testCaller(), Edit{ID: v.ID, Tags: &tags, Expected: TextFields{Tags: &startTags}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(testCaller(), v.ID)
	if err != nil || got.ADR.BodyMD != a || got.ADR.Tags != "data" {
		t.Fatalf("A's text must survive: %+v %v", got, err)
	}
}

func TestCommandsRejectForeignChangeAndPredecessor(t *testing.T) {
	t.Parallel()
	s, p := fixture(t)
	db := s.records.db
	other := projectFixture(t, db, "other", "Other")
	// sql-guarantee: Foreign Change identity for the ADR ownership fence; workflow imports adrs, so this private owner test cannot call CreateChangeIn or import its internal sqlc writer.
	foreign := completionInsert(t, db, `INSERT INTO changes(id,number,project_id,type,title,created_at) VALUES ($1,$2,$3,'feature','foreign','2026-10-09T00:00:00Z')`, fixtureNumber(), other)
	v := create(t, s, p, "local")
	for _, id := range []ids.ID{foreign, idstest.N(99999)} {
		before := snapshot(t, db)
		if got, err := s.Create(testCaller(), Create{ProjectID: p, Title: "bad", ChangeID: id}); got != nil || !errors.Is(err, errInput) {
			t.Fatalf("foreign creation: %+v %v", got, err)
		}
		if got, err := s.Edit(testCaller(), Edit{ID: v.ID, ChangeID: &id}); got != nil || !errors.Is(err, errInput) {
			t.Fatalf("foreign edit: %+v %v", got, err)
		}
		if after := snapshot(t, db); after != before {
			t.Fatal("foreign link left records")
		}
	}
	old := create(t, s, other, "foreign predecessor")
	decide(t, s, old.ID, Accept)
	// Commands reject foreign predecessors; the existing storage mutation lets
	// this owner test supply one without exposing a test-only command.
	bad, err := adrssql.New(db.SQL()).InsertADR(context.Background(), adrssql.InsertADRParams{ID: ids.New(time.Now()), ProjectID: p, Number: 2, Title: v.Title, BodyMd: v.BodyMD, Tags: v.Tags, Status: Draft.String(), SupersedesID: old.ID})
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, db)
	if got, err := s.Edit(testCaller(), Edit{ID: bad.ID, Decision: Accept}); got != nil || err == nil {
		t.Fatalf("foreign predecessor retired: %+v %v", got, err)
	}
	if after := snapshot(t, db); after != before {
		t.Fatal("foreign retirement committed")
	}
}

func testCaller() context.Context {
	return access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "alice", Via: "session"}, Verified: true})
}

func (f permitFunc) VisibleProjects(ctx context.Context, cap access.Capability) ([]ids.ID, error) {
	_, err := f(ctx, access.Permission{Capability: cap})
	if err != nil {
		return nil, err
	}
	return nil, errCallerRequired
}
