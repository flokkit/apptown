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
// clock, and leave no partial review on either missing evidence or write failure.
func TestLandingResultsAreCompleteAtomicAndClocked(t *testing.T) {
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
			err := f.service.RecordLandingResults(caller("reviewer"), id, "landed", idstest.N(12), "checked", receipts)
			if (err != nil) != (scenario == "write-failure") {
				t.Fatalf("record: %v", err)
			}
			history, err := f.service.ProjectHistory(caller("reviewer"), id, 40)
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "complete" {
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
			if err := f.service.RecordLandingResults(caller("reviewer"), id, "landed", idstest.N(12), "checked", []LandingReceipt{receipt}); err != nil {
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
