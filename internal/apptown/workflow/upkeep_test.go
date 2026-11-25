package workflow

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/database"
)

// History must limit visible entries, so daily upkeep cannot push a person's
// work off the page. Creation and closing use the owning operations.
func TestProjectHistoryGroupsBeforeLimiting(t *testing.T) {
	t.Parallel()
	for _, pattern := range [][]bool{{false, true, true, true, false, true}, append([]bool{false}, makeTrue(40)...)} {
		db := testDB(t)
		pid := workflowTestProject(t, db, "upkeep", "Upkeep", "")
		s := newService(db, allowProject(pid), Runtime{})
		base := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
		for i, system := range pattern {
			ctx := t.Context()
			who := "person"
			if system {
				ctx = access.WithCaller(ctx, access.Caller{Verified: true, Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "schedule", ProjectID: pid}})
				who = "apptown-system"
			}
			_, err := database.Transact(ctx, db, func(tx *database.Tx) (CreatedChange, error) {
				created, err := CreateChangeIn(ctx, tx, ChangeDraft{ProjectID: pid, Title: "A change"}, who, base.Add(time.Duration(i)*time.Hour))
				if err != nil {
					return created, err
				}
				action := ShipChange
				if i == len(pattern)-1 {
					action = abandonChange
				}
				_, err = TransitionChangeIn(ctx, tx, pid, created.Change.ID, ChangeMove{Action: action}, base.Add(time.Duration(i)*time.Hour))
				return created, err
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		rows, err := s.ProjectHistory(t.Context(), HistoryQuery{ProjectID: pid, Limit: 30})
		if err != nil {
			t.Fatal(err)
		}
		if len(pattern) == 6 {
			if len(rows) != 4 || rows[0].Upkeep == nil || rows[0].Upkeep.Count != 1 || rows[1].Change == nil || rows[2].Upkeep == nil || rows[2].Upkeep.Count != 3 || rows[3].Change == nil {
				t.Fatalf("islands: %+v", rows)
			}
			if !rows[2].Upkeep.From.Equal(base.Add(time.Hour)) || !rows[2].Upkeep.Until.Equal(base.Add(3*time.Hour)) {
				t.Fatalf("span: %+v", rows[2].Upkeep)
			}
		} else if len(rows) != 2 || rows[0].Upkeep == nil || rows[0].Upkeep.Count != 40 || rows[1].Change == nil || rows[1].Change.ChangeNumber != 1 {
			t.Fatalf("upkeep crowded out person: %+v", rows)
		}
		flat, err := s.ChangeHistory(t.Context(), HistoryQuery{ProjectID: pid, automatedOnly: true, From: base.Add(time.Hour), Until: base.Add(3 * time.Hour)})
		if err != nil || len(flat) != 3 || !flat[0].AutomatedOrigin {
			t.Fatalf("bounded upkeep: %+v %v", flat, err)
		}
	}
}
func makeTrue(n int) []bool {
	out := make([]bool, n)
	for i := range out {
		out[i] = true
	}
	return out
}

func TestWaitingOnNeedsPersonKeepsEveryAttentionKind(t *testing.T) {
	t.Parallel()
	kinds := map[string]bool{"person": true, "check": true, "approval": true, "ready": true, "unknown": true, "work": false, "chat": false, "queued": false, "closed": false, "future-kind": true}
	file, err := parser.ParseFile(token.NewFileSet(), "waiting_on.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	check := func(expr ast.Expr) {
		if word, ok := expr.(*ast.BasicLit); ok && word.Kind == token.STRING {
			kind, err := strconv.Unquote(word.Value)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := kinds[kind]; !ok {
				t.Errorf("attention kind %s has no needs-person expectation", kind)
			}
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				if field, ok := lhs.(*ast.SelectorExpr); ok && field.Sel.Name == "Kind" && i < len(node.Rhs) {
					if name, ok := field.X.(*ast.Ident); ok && name.Name == "w" {
						check(node.Rhs[i])
					}
				}
			}
		case *ast.CompositeLit:
			if name, ok := node.Type.(*ast.Ident); ok && name.Name == "WaitingOn" {
				for _, element := range node.Elts {
					if kv, ok := element.(*ast.KeyValueExpr); ok {
						if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Kind" {
							check(kv.Value)
						}
					}
				}
			}
		}
		return true
	})
	for kind, want := range kinds {
		if got := (WaitingOn{Kind: kind}).NeedsPerson(); got != want {
			t.Errorf("%s needs person = %v", kind, got)
		}
	}
	// The serialized fact and owner decision agree on real classifier outputs.
	for _, w := range []WaitingOn{unknownWaitingOn(), waitingOn(nil, nil), waitingOn(&Change{State: ChangeShipped}, nil)} {
		if w.PersonNeeded != w.NeedsPerson() {
			t.Fatalf("wire attention: %+v", w)
		}
	}
}

func TestUpliftTitleNamesOnlyThePacksThatMove(t *testing.T) {
	t.Parallel()
	target := adoption.Candidate{Packs: []adoption.PackPick{{Pack: "apptown", Version: 59}, {Pack: "go", Version: 6}}}
	if got := upliftTitle(target, []adoption.PackPick{{Pack: "apptown", Version: 58}, {Pack: "go", Version: 6}}, []string{"apptown v59", "go v6"}); got != "Way of working: apptown v58 → v59" {
		t.Fatal(got)
	}
	if got := upliftTitle(target, nil, []string{"apptown v59", "go v6"}); got != "Way of working: apptown v59, go v6" {
		t.Fatal(got)
	}
}

func TestQueueWorkReachesProjectReadsWithOwnerAttention(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"agent/pi", "approval", "tests"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s, ch, _, _, _ := automatedFixture(t)
			run := admitAutomatedRun(t, s, ch, kind)
			if kind == "tests" {
				steps, err := s.RunSteps(t.Context(), run.ID)
				if err != nil {
					t.Fatal(err)
				}
				claim, err := newStepAttempts(s.db, func(string) bool { return true }).start(t.Context(), steps[0], kind, ch.ProjectID)
				if err != nil {
					t.Fatal(err)
				}
				outcome, err := EncodeOutcome(StatusFail, map[string]string{"reason": "tests failed"}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := (reworkStore{db: s.db}).fail(t.Context(), claim, outcome); err != nil {
					t.Fatal(err)
				}
			}
			rows, err := s.Runs(t.Context(), RunQuery{ProjectID: ch.ProjectID, State: RunRunning, RootsOnly: true, IncludeQueueWork: true})
			if err != nil || len(rows) != 1 {
				t.Fatalf("queue root missing: %+v %v", rows, err)
			}
			r := rows[0]
			wantKind := map[string]string{"agent/pi": "work", "approval": "approval", "tests": "check"}[kind]
			if r.WaitingOn.Kind != wantKind {
				t.Fatalf("queue attention: got %s, want %s", r.WaitingOn.Kind, wantKind)
			}
			if !r.AutomatedOrigin || !r.Untouched || r.WaitingOn.NeedsPerson() != (kind != "agent/pi") || r.WaitingOn.PersonNeeded != r.WaitingOn.NeedsPerson() {
				t.Fatalf("queue facts: %+v", r)
			}
			upkeep, err := s.ProjectUpkeep(t.Context(), HistoryQuery{ProjectID: ch.ProjectID, Limit: 50})
			if err != nil || len(upkeep.Active) != 1 {
				t.Fatalf("upkeep missing root: %+v %v", upkeep, err)
			}
			ordinary, err := s.Runs(t.Context(), RunQuery{ProjectID: ch.ProjectID, State: RunRunning, RootsOnly: true})
			if err != nil || len(ordinary) != 0 {
				t.Fatalf("queue opt-in leaked: %+v %v", ordinary, err)
			}
		})
	}
}

func TestRecordedPackageMovesPersistAndRespectHumanTitle(t *testing.T) {
	t.Parallel()
	s, ch, _, _, _ := automatedFixture(t)
	run := admitAutomatedRun(t, s, ch, "routine")
	steps, err := s.RunSteps(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := newStepAttempts(s.db, func(string) bool { return true }).start(t.Context(), steps[0], "routine", ch.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	e := &Execution{service: s, ctx: t.Context(), run: run, step: steps[0], claim: claim}
	moves := []PackageMove{{Package: "node", From: "24.1.0", To: "24.1.1"}, {Package: "vitest", From: "4.1.0", To: "4.1.1"}}
	if err := e.RecordPackageMoves(moves); err != nil {
		t.Fatal(err)
	}
	// Janitor's Recent updates reads the moves through the owner's attempts.
	attempts, err := s.Attempts(t.Context(), ch.ProjectID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var got []PackageMove
	for _, a := range attempts {
		if a.ID == ch.ID {
			got = a.Packages
		}
	}
	if !reflect.DeepEqual(got, moves) {
		t.Fatalf("recorded moves: %+v", got)
	}
	current, err := ReadChangeIn(t.Context(), s.db, ch.ID)
	if err != nil || current.Title != "Update node, vitest" {
		t.Fatalf("fact title: %+v %v", current, err)
	}
	human := access.WithCaller(t.Context(), access.Caller{Verified: true, Principal: access.Principal{Kind: "user", Name: "alice", Via: "session"}})
	if _, err := s.Edit(human, ChangeEdit{ID: ch.ID, Title: &current.Title, ExpectedRevision: revision(0)}); err != nil {
		t.Fatal(err)
	}
	moves = append(moves, PackageMove{Package: "link-checker", From: "1", To: "2"})
	if err := e.RecordPackageMoves(moves); err != nil {
		t.Fatal(err)
	}
	current, err = ReadChangeIn(t.Context(), s.db, ch.ID)
	if err != nil || current.Title != "Update node, vitest" {
		t.Fatalf("human title replaced: %+v %v", current, err)
	}
}

func TestSentBackAppTownWorkRemainsIndividualWhileOpen(t *testing.T) {
	t.Parallel()
	store, source, _ := completedSendBackFixture(t)
	// sql-guarantee: set historical automated origin on this recovery fixture; no command changes the origin of an existing change.
	completionExec(t, store.db, `UPDATE changes SET automated=true,automated_origin=true WHERE id=$1`, source.ChangeID)
	s := newService(store.db, allowProject(source.ProjectID), Runtime{})
	result, err := store.sendBack(context.Background(), source.ChangeID, "Please fix the validation", "alice", "", source.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.touchChange(access.WithCaller(t.Context(), access.Caller{Verified: true, Principal: access.Principal{Kind: "user", Name: "alice", Via: "session"}}), source.ProjectID, source.ChangeID); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Runs(t.Context(), RunQuery{ProjectID: source.ProjectID, State: RunRunning, RootsOnly: true, IncludeQueueWork: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == result.RunID {
			if !row.AutomatedOrigin || row.Untouched {
				t.Fatalf("sent-back origin/touch: %+v", row)
			}
			return
		}
	}
	t.Fatal("sent-back row missing")
}
