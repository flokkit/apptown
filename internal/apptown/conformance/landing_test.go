package conformance

import (
	"encoding/json"
	"github.com/flokkit/apptown/internal/check"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/stamp"
)

// Recording receipts must preserve unknowns and tool exceptions, use the owner's
// clock, and keep available receipts atomic even when other evidence is missing.
func TestLandingResultsArePartialAtomicAndClocked(t *testing.T) {
	for _, scenario := range []string{"complete", "missing", "write-failure"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newProjectionFixture(t)
			id := f.project(t, "landing", "rules", 2, noFixme+","+strings.Replace(noFixme, "no-fixme", "second", 1))
			at := time.Date(2025, 4, 5, 6, 7, 8, 0, time.UTC)
			f.service.records.clock = func() time.Time { return at }
			receipts := []LandingReceipt{{Block: "rules", Version: 2, Check: "no-fixme", Verdict: "pass", EvaluatorVer: "image-digest"}}
			if scenario != "missing" {
				receipts = append(receipts, LandingReceipt{Block: "rules", Version: 2, Check: "second", Verdict: "pass", EvaluatorVer: "image-digest"})
			}
			if scenario == "write-failure" {
				dbtest.FailNextStatement(t, f.db, "-- name: CompleteLandingReview :execrows")
			}
			err := f.service.RecordLandingResults(caller("reviewer"), id, idstest.N(7), "landed", idstest.N(12), "checked", receipts)
			if (err != nil) != (scenario == "write-failure") {
				t.Fatalf("record: %v", err)
			}
			history, err := f.service.ProjectHistory(caller("reviewer"), id, 40)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "write-failure" {
				all, err := f.service.Reviews(caller("reviewer"), 40)
				if err != nil || len(all) != 0 || len(history) != 0 {
					t.Fatalf("partial records: %+v %+v %v", all, history, err)
				}
				return
			}
			if len(history) != 1 {
				t.Fatalf("saved history: %+v", history)
			}
			detail, err := f.service.Review(caller("reviewer"), history[0].ReviewID)
			if err != nil {
				t.Fatal(err)
			}
			if detail.Review.StartedAt != stamp.Of(at).String() || detail.Review.FinishedAt.String() != stamp.Of(at).String() || detail.Verdicts[0].Record.At != stamp.Of(at).String() {
				t.Fatalf("owner clock: %+v %+v", detail.Review, detail.Verdicts)
			}
		})
	}
}

func TestLandingResultsPreserveEveryReceiptOutcome(t *testing.T) {
	for _, tc := range []struct {
		receipt string
		want    Outcome
	}{
		{"pass", Conforms}, {"fail", DoesNotConform},
		{"excepted", Excepted}, {"not-applicable", NotApplicable},
		{"skipped", CannotAssess}, {"cannot-assess", CannotAssess},
		{"unclear", CannotAssess}, {"error", EvaluationError},
	} {
		t.Run(tc.receipt, func(t *testing.T) {
			t.Parallel()
			f := newProjectionFixture(t)
			id := f.project(t, "landing", "rules", 1, noFixme)
			receipt := LandingReceipt{AgentEvidence: &check.AgentEvidence{AppTownFault: tc.receipt == "cannot-assess", Session: "retained-session", Commit: "checked", Tree: "exact-tree", DiffHash: "exact-diff", Runtime: "runtime-contract", TemplateHash: "template", RequestedModel: "requested", ServedModel: "served", TokensIn: 100, TokensOut: 20, DurationMs: 42, Reused: true}, Block: "rules", Version: 1, Check: "no-fixme", Verdict: tc.receipt, Detail: "saved detail", EvaluatorVer: "exact-evaluator", EvidenceHash: "exact-evidence"}
			if tc.receipt == "excepted" {
				receipt.ExceptedBy = `{"id":"` + idstest.N(33).String() + `","status":"accepted-risk","by":"decider"}`
			}
			if err := f.service.RecordLandingResults(caller("reviewer"), id, idstest.N(7), "landed", idstest.N(12), "checked", []LandingReceipt{receipt}); err != nil {
				t.Fatal(err)
			}
			f.source.move(id, "landed")
			view, err := f.service.View(caller("reviewer"), ids.Nil)
			if err != nil {
				t.Fatal(err)
			}
			rows := view.ByProject["landing"]
			if len(rows) != 1 || rows[0].Record.State != tc.want || rows[0].Record.Detail != receipt.Detail || rows[0].Record.ExceptedBy != receipt.ExceptedBy || rows[0].Record.EvaluatorVer != receipt.EvaluatorVer || rows[0].Record.EvidenceHash != receipt.EvidenceHash || !reflect.DeepEqual(rows[0].Record.AgentEvidence, receipt.AgentEvidence) {
				t.Fatalf("receipt changed: %+v", rows)
			}
		})
	}
}

// Landing provenance uses the existing trigger and retains the source identities.
func TestLandingReviewJSONUsesTrigger(t *testing.T) {
	body, err := json.Marshal(Review{Status: ReviewDone, Trigger: "landing", SourceRun: idstest.N(12), Candidate: "checked"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["kind"]; ok {
		t.Fatalf("duplicate review kind: %s", body)
	}
	if string(fields["trigger"]) != `"landing"` || string(fields["sourceRun"]) != `"`+idstest.N(12).String()+`"` || string(fields["candidate"]) != `"checked"` {
		t.Fatalf("landing provenance: %s", body)
	}
}

// A partial landing fixes the check it ran and leaves the other finding's
// exact verdict and assessment date available in the current projection.
func TestLandingFixIsImmediateAndMissingChecksKeepTheirVerdict(t *testing.T) {
	t.Parallel()
	f := newProjectionFixture(t)
	id := f.project(t, "landing", "rules", 1, noFixme+","+strings.Replace(noFixme, "no-fixme", "second", 1))
	ctx := caller("reviewer")
	_, before := f.review(t, ctx, "finding", []ids.ID{id}, outcomes(map[string]Outcome{"landing": DoesNotConform}))
	if err := f.service.RecordLandingResults(ctx, id, idstest.N(7), "landed", idstest.N(12), "checked", []LandingReceipt{{Block: "rules", Version: 1, Check: "no-fixme", Verdict: "pass"}}); err != nil {
		t.Fatal(err)
	}
	f.source.move(id, "landed")
	fixes, err := f.service.ProjectFixes(ctx, id, 40)
	if err != nil || len(fixes) != 1 {
		t.Fatalf("immediate fix: %+v %v", fixes, err)
	}
	fix := fixes[0]
	if len(fix.ChangeIDs) != 1 || fix.ChangeIDs[0] != idstest.N(7) || fix.Commits != "landed" || fix.AfterSHA != "landed" || fix.BeforeState != DoesNotConform {
		t.Fatalf("fix attribution: %+v", fix)
	}
	view := f.view(t, ids.Nil)
	if len(view.ByProject["landing"]) != 2 {
		t.Fatalf("current checks: %+v", view.ByProject["landing"])
	}
	for _, row := range view.ByProject["landing"] {
		if row.Check == "second" {
			for _, old := range before {
				if old.Check == "second" && (row.Record.ID != old.ID || row.Record.At != old.At) {
					t.Fatalf("missing check changed: %+v, previous %+v", row, old)
				}
			}
		}
	}
	v, err := f.service.LandingVerdict(ctx, id, idstest.N(12), CheckIdentity{Block: "rules", Version: 1, Check: "no-fixme"})
	if err != nil || v == nil || v.State != Conforms || v.ReviewID != fix.ReviewID {
		t.Fatalf("landing check: %+v %v", v, err)
	}
	missing, err := f.service.LandingVerdict(ctx, id, idstest.N(12), CheckIdentity{Block: "rules", Version: 1, Check: "second"})
	if err != nil || missing != nil {
		t.Fatalf("missing landing check: %+v %v", missing, err)
	}
}
