package apptown

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

func narrativeBuilderFixture(t *testing.T) (*database.DB, *workflow.Service, workflow.NarrativeSource) {
	t.Helper()
	db := testDB(t)
	testAccounts(t, db, "test", "alice")
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "narrative"})
	})
	if err != nil {
		t.Fatal(err)
	}
	work := workflow.New(context.Background(), db, workflow.Dependencies{Authority: testActor{}, Narratives: narrativeReleaseNotes{}})
	t.Cleanup(work.Stop)
	ch, err := work.Create(asPerson("test"), workflow.ChangeDraft{ProjectID: pid, Title: "automatic", Body: "brief", TitleFromChat: true})
	if err != nil {
		t.Fatal(err)
	}
	if applied, err := work.NameFromChat(context.Background(), pid, ch.ID, "automatic", "test"); err != nil || !applied {
		t.Fatalf("initial title: %v %v", applied, err)
	}
	source, err := work.NarrativeSource(asPerson("test"), ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := work.PublishNarrative(asPerson("test"), workflow.NarrativePublication{Source: source, Draft: workflow.NarrativeDraft{Note: "previous"}}); err != nil {
		t.Fatal(err)
	}
	source, err = work.NarrativeSource(asPerson("test"), ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	db.Pool().SetMaxOpenConns(1)
	return db, work, source
}

func TestNarrativeBuilderAccountsForRejectedAndEmptyResults(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "empty", "changed", "receipt", "cancelled", "evidence failure"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			db, work, source := narrativeBuilderFixture(t)
			calls := 0
			ctx, cancel := context.WithTimeout(asPerson("test"), 5*time.Second)
			defer cancel()
			// The call goes through the real gateway to a fake provider, so
			// its spend is the gateway's line, written whatever publication
			// then does.
			pattern := fakeModelProvider(t, integration.NewWithClock(db, nil), func(q providerQuestion) (int, string) {
				calls++
				if q.Model != "judge" || !strings.Contains(q.User, "LIVE EVIDENCE") || !strings.Contains(q.User, "TITLE: automatic") || !strings.Contains(q.User, "RUNS:") {
					t.Errorf("model evidence: %q %q", q.Model, q.User)
				}
				if !strings.Contains(q.System, "ASKED:") {
					t.Errorf("the writer is not asked whether the request is met: %q", q.System)
				}
				switch mode {
				case "changed":
					body, rev := "changed while thinking", source.Change.EditRevision
					if _, err := work.Edit(asPerson("alice"), workflow.ChangeEdit{ProjectID: source.Change.ProjectID, ID: source.Change.ID, Body: &body, ExpectedRevision: &rev}); err != nil {
						t.Error(err)
					}
				case "cancelled":
					// The caller gives up while the provider is thinking.
					cancel()
					<-q.Request.Context().Done()
					return 200, ""
				case "receipt":
					dbtest.FailNextStatement(t, db, "INSERT INTO audit_log")
				}
				out := "TITLE: generated\nASKED: no — the visible heading was asked for; only the tab title changed\n\nGenerated note"
				if mode == "empty" {
					out = "TITLE: title without note"
				}
				return 200, chatAnswer(out, 11, 7, 0.03)
			})
			b := narrativeBuilder{work: work, models: gatewayOver(t, db),
				resolve: func(context.Context, ids.ID) (string, string, error) { return pattern, "fast", nil },
				evidence: func(ctx context.Context, got workflow.NarrativeSource) (narrativeEvidence, error) {
					// A second read with one connection proves Source released its snapshot.
					if _, err := work.Change(ctx, got.Change.ID); err != nil {
						return narrativeEvidence{}, err
					}
					if mode == "evidence failure" {
						return narrativeEvidence{}, errors.New("source unavailable")
					}
					return narrativeEvidence{Text: "LIVE EVIDENCE"}, nil
				},
			}
			err := b.refresh(ctx, source.Change.ID)
			if (err == nil) != (mode == "success") {
				t.Fatalf("refresh %s: %v", mode, err)
			}
			want := 1
			if mode == "evidence failure" {
				want = 0
			}
			if calls != want {
				t.Fatalf("model calls %d want %d", calls, want)
			}
			// A call cut off after it was sent owes unknown spend; every
			// answered call owes what the provider reported.
			wantTotals := usage.Totals{Input: int64(want * 11), Output: int64(want * 7), Total: int64(want * 18), CostMicros: int64(want * 30000)}
			if mode == "cancelled" {
				wantTotals = usage.Totals{Incomplete: 1}
			}
			report, err := usage.New(db).Summary(context.Background(), 1, source.Change.ProjectID, "")
			if got := report.Totals; err != nil || got.Input != wantTotals.Input || got.Output != wantTotals.Output || got.Total != wantTotals.Total || got.CostMicros != wantTotals.CostMicros || got.Incomplete != wantTotals.Incomplete {
				t.Fatalf("incurred usage: %+v %v", report.Totals, err)
			}
			receipts := 0
			if mode == "success" {
				receipts = 1
			}
			entries, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: "change.narrative-updated"})
			if err != nil || len(entries) != receipts+1 {
				t.Fatalf("publication receipts: %+v %v", entries, err)
			}
			if mode == "success" {
				ch, err := work.Change(asPerson("test"), source.Change.ID)
				if err != nil || ch.Summary != "Generated note" || ch.RequestMet != workflow.RequestMetNo || ch.RequestGap != "the visible heading was asked for; only the tab title changed" {
					t.Fatalf("published reading of the request: %+v %v", ch, err)
				}
			}
			if mode != "success" {
				ch, err := work.Change(asPerson("test"), source.Change.ID)
				if err != nil || ch.Title != "automatic" || ch.Summary != "previous" || ch.RequestMet != workflow.RequestMetUnknown {
					t.Fatalf("failed generation changed prose: %+v %v", ch, err)
				}
			}
		})
	}
}

func TestNarrativeRefreshHTTPSeparatesStorageFailureFromAbsence(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "bls")

	pid := createReadTestProject(t, a, "shop")
	id := createReadTestChange(t, a, pid, "original")
	dbtest.FailNextStatement(t, a.DB, "-- name: NarrativeRunsMeta")
	response := changeRequest(a, "bls", "POST", fmt.Sprintf("/api/v1/changes/%d/distill", id), `{}`)
	if response.Code != 500 {
		t.Fatalf("failed evidence HTTP %d: %s", response.Code, response.Body.String())
	}
	if missing := changeRequest(a, "bls", "POST", fmt.Sprintf("/api/v1/changes/%d/distill", idstest.N(999999)), `{}`); missing.Code != 404 {
		t.Fatalf("unknown Change HTTP %d: %s", missing.Code, missing.Body.String())
	}
	report, err := a.Usage.Ledger.Summary(context.Background(), 1, pid, "")
	if err != nil || report.Totals.Total != 0 {
		t.Fatalf("failed evidence spent tokens: %+v %v", report, err)
	}
	entries, err := audit.Recent(context.Background(), a.DB, audit.Filter{KindPrefix: "change.narrative-updated"})
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed evidence published: %+v %v", entries, err)
	}
}

// This is the composition promise: Deployment participates in Work's actual
// transaction, so a release-note failure cannot leave a new Change narrative.
func TestNarrativePublicationIncludesReleaseNotesAtomically(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	testAccounts(t, db, "test")
	db.Pool().SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	created, err := database.Transact(ctx, db, func(tx *database.Tx) (workflow.CreatedChange, error) {
		pid, err := project.CreateAt(ctx, tx, time.Now(), project.Spec{Slug: "release-note"})
		if err != nil {
			return workflow.CreatedChange{}, err
		}
		ch, err := workflow.CreateChangeIn(ctx, tx, workflow.ChangeDraft{ProjectID: pid, Title: "automatic", TitleFromChat: true}, "test", time.Now())
		if err == nil {
			_, err = workflow.TransitionChangeIn(ctx, tx, pid, ch.Change.ID, workflow.ChangeMove{Action: workflow.ShipChange}, time.Now())
		}
		return ch, err
	})
	if err != nil {
		t.Fatal(err)
	}
	ch := created.Change
	owner := workflow.New(context.Background(), db, workflow.Dependencies{Authority: testActor{}, Narratives: narrativeReleaseNotes{}})
	t.Cleanup(owner.Stop)
	if applied, err := owner.NameFromChat(ctx, ch.ProjectID, ch.ID, "automatic", "test"); err != nil || !applied {
		t.Fatalf("title: %v %v", applied, err)
	}
	releases := deployment.NewReleases(db)
	release, err := releases.Start(ctx, deployment.ReleaseDraft{Source: deployment.ReleaseFromBranch, ProjectID: ch.ProjectID, Deployment: "main", Actor: "test"}, "shipped work")
	if err != nil {
		t.Fatal(err)
	}
	ctx = asPerson("test")
	source, err := owner.NarrativeSource(ctx, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	in := workflow.NarrativePublication{Source: source, Draft: workflow.NarrativeDraft{Title: "generated", Note: "new note"}}
	dbtest.FailNextStatement(t, db, "-- name: FillChangeReleaseNotes")
	if err := owner.PublishNarrative(ctx, in); err == nil {
		t.Fatal("release failure did not reject publication")
	}
	after, err := owner.NarrativeSource(ctx, ch.ID)
	if err != nil || after.Change.Title != "automatic" || after.Change.Summary != "" {
		t.Fatalf("partial change: %+v %v", after, err)
	}
	if err := owner.PublishNarrative(ctx, in); err != nil {
		t.Fatal(err)
	}
	published, err := releases.ByNumber(ctx, ch.ProjectID, release.Number)
	if err != nil || published.Notes != "new note" {
		t.Fatalf("linked release prose: %+v %v", published, err)
	}
	if err := owner.PublishNarrative(ctx, in); !errors.Is(err, workflow.ErrNarrativeChanged) {
		t.Fatalf("stale publication replayed: %v", err)
	}
	entries, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: "change.narrative-updated"})
	if err != nil || len(entries) != 1 {
		t.Fatalf("publication receipt: %+v %v", entries, err)
	}
}

func TestEmptyCommittedNarrativeSkipsModelAndPreservesPublicationChecks(t *testing.T) {
	t.Parallel()
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			t.Parallel()
			_, work, source := narrativeBuilderFixture(t)
			b := narrativeBuilder{work: work,
				resolve: func(context.Context, ids.ID) (string, string, error) {
					t.Fatal("empty diff resolved a model")
					return "", "", nil
				},
				evidence: func(ctx context.Context, got workflow.NarrativeSource) (narrativeEvidence, error) {
					if changed {
						body, rev := "changed during comparison", got.Change.EditRevision
						if _, err := work.Edit(ctx, workflow.ChangeEdit{ID: got.Change.ID, ProjectID: got.Change.ProjectID, Body: &body, ExpectedRevision: &rev}); err != nil {
							t.Fatal(err)
						}
					}
					return narrativeEvidence{Empty: true, Text: "The helper claims it implemented everything."}, nil
				},
				models: askFunc(func(context.Context, modelcall.Call) (string, *modelcall.Usage, error) {
					t.Fatal("empty diff called a model")
					return "", nil, nil
				}),
			}
			err := b.refresh(asPerson("test"), source.Change.ID)
			if changed && !errors.Is(err, workflow.ErrNarrativeChanged) || !changed && err != nil {
				t.Fatalf("publication: %v", err)
			}
			ch, err := work.Change(asPerson("test"), source.Change.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := workflow.EmptyCommittedChange
			if changed {
				want = "previous"
			}
			if ch.Summary != want || ch.Title != "automatic" {
				t.Fatalf("empty narrative changed meaning/title: %+v", ch)
			}
		})
	}
}
