package scm

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

type fixtureAuthorityKey struct{}
type fixtureWork struct {
	db    *database.DB
	claim *fixtureClaim
}

func (w fixtureWork) Prepare(ctx context.Context, p Purpose, id ids.ID) (Claim, error) {
	authority, _ := ctx.Value(fixtureAuthorityKey{}).(Purpose)
	if authority == 0 || p == NewEffect && authority != NewEffect || id != w.claim.source.RunID {
		return nil, ErrCaller
	}
	claim := *w.claim
	var err error
	claim.revision, claim.closed, err = readFixtureWork(ctx, w.db.SQL(), id, false)
	return &claim, err
}
func (w fixtureWork) RequireInspection(ctx context.Context) error {
	if authority, _ := ctx.Value(fixtureAuthorityKey{}).(Purpose); authority != Observe {
		return ErrCaller
	}
	return ctx.Err()
}
func authorized(p Purpose) context.Context {
	return context.WithValue(context.Background(), fixtureAuthorityKey{}, p)
}

type fixtureDriver struct {
	prepare func(context.Context, Source, string) (Prepared, error)
	execute func(context.Context, Source, Prepared, string) (Execution, error)
	inspect func(context.Context, Source, Record) (Inspection, error)
}

func (d fixtureDriver) Prepare(c context.Context, s Source, old string) (Prepared, error) {
	if d.prepare == nil {
		return Prepared{}, fmt.Errorf("unexpected preparation")
	}
	return d.prepare(c, s, old)
}
func (d fixtureDriver) Execute(c context.Context, s Source, p Prepared, op string) (Execution, error) {
	if d.execute == nil {
		return Execution{}, fmt.Errorf("unexpected execution")
	}
	return d.execute(c, s, p, op)
}
func (d fixtureDriver) Inspect(c context.Context, s Source, r Record) (Inspection, error) {
	if d.inspect == nil {
		return Inspection{}, fmt.Errorf("unexpected inspection")
	}
	return d.inspect(c, s, r)
}
func serviceFixture(t *testing.T) (*Service, *fixtureClaim) {
	t.Helper()
	store, claim := mergeFixture(t)
	return New(store.db, fixtureWork{store.db, claim}, &RepositoryLocks{}, nil), claim
}
func TestServiceRequiresExplicitOperationAuthority(t *testing.T) {
	t.Parallel()
	service, claim := serviceFixture(t)
	for _, ctx := range []context.Context{context.Background(), authorized(Observe)} {
		if _, err := service.Merge(ctx, claim.source.RunID, fixtureDriver{}); !errors.Is(err, ErrCaller) {
			t.Fatalf("unauthorized merge: %v", err)
		}
	}
	if _, err := service.Read(context.Background(), claim.source.RunID); !errors.Is(err, ErrCaller) {
		t.Fatal(err)
	}
	if _, err := service.Inspect(context.Background(), claim.source.RunID, fixtureDriver{}); !errors.Is(err, ErrCaller) {
		t.Fatal(err)
	}
	for _, ctx := range []context.Context{context.Background(), authorized(NewEffect)} {
		if _, err := service.InspectPending(ctx, fixtureDriver{}); !errors.Is(err, ErrCaller) {
			t.Fatalf("scope granted global inspection: %v", err)
		}
	}
	if _, err := service.Merge(authorized(NewEffect), idstest.N(999999), fixtureDriver{}); !errors.Is(err, ErrCaller) {
		t.Fatal(err)
	}
	wantStoredMerge(t, service.records.db, claim.source.RunID, false)
	wantStoredMerge(t, service.records.db, idstest.N(999999), false)
}
func TestServiceCommitsAdmissionBeforeEffectAndRequiredReceiptAfter(t *testing.T) {
	t.Parallel()
	service, claim := serviceFixture(t)
	service.records.db.Pool().SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(authorized(NewEffect), 5*time.Second)
	defer cancel()
	calls := 0
	driver := fixtureDriver{
		prepare: func(ctx context.Context, s Source, old string) (Prepared, error) {
			calls++
			record, err := service.Read(ctx, s.RunID)
			if err != nil || record.State != Idle {
				t.Fatalf("prepare record: %+v %v", record, err)
			}
			return Prepared{Kind: LocalMerge, Expected: "base"}, nil
		},
		execute: func(ctx context.Context, s Source, p Prepared, op string) (Execution, error) {
			calls++
			record, err := service.Read(ctx, s.RunID)
			if err != nil || record.State != Pending || record.Operation != op || record.Expected != p.Expected {
				t.Fatalf("effect preceded admission: %+v %v", record, err)
			}
			wantFixtureReceipts(t, service.records.db, "", 0)
			return Execution{Result: "result"}, nil
		},
	}
	result, err := service.Merge(ctx, claim.source.RunID, driver)
	if err != nil || !result.Merged || result.SHA != "result" || result.Operation == "" || calls != 2 {
		t.Fatalf("merge: %+v %v calls=%d", result, err, calls)
	}
	record, err := service.Read(ctx, claim.source.RunID)
	if err != nil || record.State != Recorded || record.Result != result.SHA || record.Operation != result.Operation {
		t.Fatalf("receipt: %+v %v", record, err)
	}
	if sha := fixtureChangeSHA(t, service.records.db, claim.changeID); sha != "result" {
		t.Fatalf("change source: %q", sha)
	}
	wantFixtureReceipts(t, service.records.db, "merge.completed", 1)
}
func TestServiceAdmissionFailuresNeverExecute(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"write", "suppressed", "commit", "work moved", "work closed", "driver refuses"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			service, claim := serviceFixture(t)
			db := service.records.db
			driver := fixtureDriver{prepare: func(context.Context, Source, string) (Prepared, error) {
				switch failure {
				case "driver refuses":
					// A refusal found while preparing (a merge request's
					// settings, a moved main) asks the provider nothing.
					return Prepared{}, fmt.Errorf("%w: plain merge commits", ErrMergeRequestSettings)
				case "write":
					dbtest.FailNextStatement(t, db, "INSERT INTO run_merges")
				case "work moved":
					// sql-guarantee: move the test-only participant revision during SCM's external prepare callback.
					completionExec(t, db, `UPDATE fixture_work SET revision=revision+1`)
				case "work closed":
					// sql-guarantee: close the test-only participant during SCM's external prepare callback.
					completionExec(t, db, `UPDATE fixture_work SET closed=true`)
				default:
					body, trigger := "RETURN NULL;", "CREATE TRIGGER veto BEFORE INSERT ON run_merges"
					if failure == "commit" {
						body = "RAISE EXCEPTION 'deferred admission refusal';"
						trigger = "CREATE CONSTRAINT TRIGGER veto AFTER INSERT ON run_merges DEFERRABLE INITIALLY DEFERRED"
					}
					completionExec(t, db, `CREATE FUNCTION veto() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$`)
					completionExec(t, db, trigger+` FOR EACH ROW EXECUTE FUNCTION veto()`)
				}
				return Prepared{Kind: LocalMerge, Expected: "base"}, nil
			}, execute: func(context.Context, Source, Prepared, string) (Execution, error) {
				t.Error("effect ran without committed admission")
				return Execution{}, nil
			}}
			result, err := service.Merge(authorized(NewEffect), claim.source.RunID, driver)
			if err == nil || result.Merged {
				t.Fatalf("failed admission: %+v %v", result, err)
			}
			wantStoredMerge(t, db, claim.source.RunID, false)
			wantFixtureReceipts(t, db, "", 0)
		})
	}
}
func TestServiceKeepsUnprovenEffectsPending(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"effect error", "missing result", "receipt error", "proven conflict", "proven no effect", "contradictory result", "unproven conflict"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			service, claim := serviceFixture(t)
			driver := fixtureDriver{prepare: func(context.Context, Source, string) (Prepared, error) {
				return Prepared{Kind: LocalMerge, Expected: "base"}, nil
			}, execute: func(context.Context, Source, Prepared, string) (Execution, error) {
				switch outcome {
				case "contradictory result":
					return Execution{Unchanged: true, Result: "effect"}, nil
				case "unproven conflict":
					return Execution{Conflicts: []string{"file"}}, nil
				case "effect error":
					return Execution{}, errors.New("transport lost")
				case "missing result":
					return Execution{}, nil
				case "receipt error":
					dbtest.FailNextStatement(t, service.records.db, "INSERT INTO run_events")
					return Execution{Result: "effect"}, nil
				case "proven conflict":
					return Execution{Unchanged: true, Conflicts: []string{"file"}}, nil
				default:
					return Execution{Unchanged: true}, errors.New("no effect")
				}
			}}
			result, err := service.Merge(authorized(NewEffect), claim.source.RunID, driver)
			if result.Merged || (outcome != "proven conflict" && err == nil) {
				t.Fatalf("unproven success: %+v %v", result, err)
			}
			record, readErr := service.Read(authorized(Observe), claim.source.RunID)
			want := Pending
			if outcome == "proven conflict" || outcome == "proven no effect" {
				want = Idle
			}
			if readErr != nil || record.State != want {
				t.Fatalf("record: %+v %v", record, readErr)
			}
			wantFixtureReceipts(t, service.records.db, "merge.completed", 0)
		})
	}
}
func TestServiceInspectionNeverReplaysAndRequiresAudit(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"found", "unchanged", "unknown", "list unavailable", "metadata", "audit unavailable"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			pending, store := pendingMergeFixture(t)
			claim := pending.work.(*fixtureClaim)
			service := New(store.db, fixtureWork{store.db, claim}, &RepositoryLocks{}, nil)
			driver := fixtureDriver{inspect: func(context.Context, Source, Record) (Inspection, error) {
				switch outcome {
				case "found":
					return Inspection{Kind: Found, Result: "proven"}, nil
				case "unchanged":
					return Inspection{Kind: Unchanged}, nil
				default:
					return Inspection{Kind: Unresolved}, nil
				}
			}}
			switch outcome {
			case "list unavailable":
				dbtest.FailNextStatement(t, store.db, "-- name: PendingMerges :many")
			case "metadata":
				completionExec(t, store.db, `ALTER TABLE run_merges DROP CONSTRAINT run_merge_tuple`)
				replaceStoredMerge(t, store.db, pending.source.RunID, func(i *intent) { i.Expected = "" })
			case "audit unavailable":
				dbtest.FailNextStatement(t, store.db, "INSERT INTO audit_log")
			}
			summary, err := service.InspectPending(authorized(Observe), driver)
			switch outcome {
			case "found":
				if err != nil || summary.Recorded != 1 {
					t.Fatalf("inspection: %+v %v", summary, err)
				}
			case "unchanged":
				if err != nil || summary.Cancelled != 1 {
					t.Fatalf("inspection: %+v %v", summary, err)
				}
			case "unknown":
				if err != nil || summary.Unresolved != 1 {
					t.Fatalf("inspection: %+v %v", summary, err)
				}
				if receipts := ledger(t, store.db, "landing.merge-unknown"); len(receipts) != 1 {
					t.Fatalf("unknown-merge receipts: %+v", receipts)
				}
			default:
				if err == nil {
					t.Fatal("failed inspection returned success")
				}
				if receipts := ledger(t, store.db, ""); len(receipts) != 0 {
					t.Fatalf("failed inspection left receipts: %+v", receipts)
				}
			}
			wantFixtureReceipts(t, store.db, "run.done", 0)
		})
	}
}
func TestServiceRejectsDriverPlansThatChangeProvider(t *testing.T) {
	t.Parallel()
	for _, provider := range []bool{false, true} {
		t.Run(fmt.Sprint(provider), func(t *testing.T) {
			t.Parallel()
			service, claim := serviceFixture(t)
			kind := ProviderMerge
			if provider {
				claim.source.MR = MergeRequest{Project: "one/repo", IID: 3}
				kind = LocalMerge
			}
			driver := fixtureDriver{prepare: func(context.Context, Source, string) (Prepared, error) {
				return Prepared{Kind: kind, Expected: "source"}, nil
			}}
			if result, err := service.Merge(authorized(NewEffect), claim.source.RunID, driver); err == nil || result.Merged {
				t.Fatalf("provider replaced: %+v %v", result, err)
			}
			wantStoredMerge(t, service.records.db, claim.source.RunID, false)
		})
	}
}

func TestInspectionDoesNotRelabelALocalIntentAsAProviderMerge(t *testing.T) {
	t.Parallel()
	pending, store := pendingMergeFixture(t)
	claim := *pending.work.(*fixtureClaim)
	// Work may acquire an MR artifact after the local process was interrupted.
	// That artifact cannot change which effect the old intent describes.
	claim.source.MR = MergeRequest{Project: "group/repo", IID: 3}
	service := New(store.db, fixtureWork{store.db, &claim}, &RepositoryLocks{}, nil)
	driver := fixtureDriver{inspect: func(context.Context, Source, Record) (Inspection, error) {
		return Inspection{Kind: Found, Result: "local-effect"}, nil
	}}
	if _, err := service.InspectPending(authorized(Observe), driver); err != nil {
		t.Fatal(err)
	}
	completed := fixtureReceipts(t, store.db, "merge.completed")
	if len(completed) != 1 {
		t.Fatalf("completion receipts: %+v", completed)
	}
	if _, via := completed[0]["via"]; via {
		t.Fatalf("local intent relabelled: %+v", completed[0])
	}
	if _, iid := completed[0]["iid"]; iid {
		t.Fatalf("local intent relabelled: %+v", completed[0])
	}
}

// TestARecordedLandingNeverMergesAgain: a landing merges once. Once its merge
// is recorded a later landing may pass it (the landing slot is released), so a
// repinned candidate must not merge behind that landing's back — locally or
// at the provider — and the recorded result is kept.
func TestARecordedLandingNeverMergesAgain(t *testing.T) {
	t.Parallel()
	for _, kind := range []PlanKind{LocalMerge, ProviderMerge, Existing} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			t.Parallel()
			service, claim := serviceFixture(t)
			ctx := authorized(NewEffect)
			first := fixtureDriver{
				prepare: func(context.Context, Source, string) (Prepared, error) {
					return Prepared{Kind: LocalMerge, Expected: "base"}, nil
				},
				execute: func(context.Context, Source, Prepared, string) (Execution, error) {
					return Execution{Result: "first"}, nil
				},
			}
			if r, err := service.Merge(ctx, claim.source.RunID, first); err != nil || r.SHA != "first" {
				t.Fatalf("first merge: %+v %v", r, err)
			}
			again := fixtureDriver{
				prepare: func(context.Context, Source, string) (Prepared, error) {
					// a repinned candidate: a new effect, not the recorded result
					return Prepared{Kind: kind, Expected: "first", Result: "second"}, nil
				},
				execute: func(context.Context, Source, Prepared, string) (Execution, error) {
					t.Error("a recorded landing merged again")
					return Execution{Result: "second"}, nil
				},
			}
			if _, err := service.Merge(ctx, claim.source.RunID, again); !errors.Is(err, ErrMergeFinal) {
				t.Fatalf("second merge of a recorded landing: %v, want ErrMergeFinal", err)
			}
			record, err := service.Read(ctx, claim.source.RunID)
			if err != nil || record.State != Recorded || record.Result != "first" {
				t.Fatalf("recorded result changed: %+v %v", record, err)
			}
		})
	}
}
