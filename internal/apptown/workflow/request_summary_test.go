package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

func TestRequestSummaryPublicationStopsAtAcceptanceAndRequiresReceipt(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "accepted", "closed", "receipt failure", "human request"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			db, change := changeEditFixture(t, ChangeDraft{Title: "A change", TitleFromChat: mode != "human request"})
			run := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: change.ProjectID, ChangeID: change.ID, WorkflowName: "test", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: json.RawMessage(`{"kind":"pipeline","role":"working","stages":[{"name":"finish"}]}`)})
			approval := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "accept", Type: "approval", Status: StatusPending.String()})
			ctx := context.Background()
			s := newService(db, allowProject(run.ProjectID), Runtime{})
			t.Cleanup(s.Stop)
			source, err := s.NarrativeSource(ctx, run.ChangeID)
			if err != nil {
				t.Fatal(err)
			}
			summary := requestSummary{Text: "Make distant cards easy to reach.", Model: "quick", Slot: "fast", Messages: []RequestMessage{{Session: "chat", Index: 1, Text: "Bring cards into view."}}}
			switch mode {
			case "accepted":
				people(t, db, "alice")
				if _, err := voteForTest(db, approval, "alice", VoteApprove, ""); err != nil {
					t.Fatal(err)
				}
			case "closed":
				// Use the lifecycle owner to close the fixture without inventing a landing.
				if err := db.InTx(ctx, func(tx *database.Tx) error {
					_, err := TransitionChangeIn(ctx, tx, run.ProjectID, run.ChangeID, ChangeMove{Action: abandonChange}, time.Now())
					return err
				}); err != nil {
					t.Fatal(err)
				}
			case "receipt failure":
				dbtest.FailNextStatement(t, db, "INSERT INTO audit_log")
			}
			open, err := s.requestSummaryOpen(ctx, source)
			if err != nil || open != (mode == "success" || mode == "receipt failure") {
				t.Fatalf("open %v %v", open, err)
			}
			err = s.publishRequestSummary(ctx, source, "Reach distant cards", summary)
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("published after acceptance, closure, failed receipt or on a human request")
			}
			got, err := s.Change(ctx, run.ChangeID)
			if err != nil {
				t.Fatal(err)
			}
			if (got.RequestSummarySource != nil) != (mode == "success") {
				t.Fatalf("stored summary %+v", got.RequestSummarySource)
			}
			if mode == "success" {
				if err := s.publishRequestSummary(ctx, source, "Stale writer", summary); !errors.Is(err, ErrNarrativeChanged) {
					t.Fatalf("stale writer %v", err)
				}
				if got.RequestSummarySource.Model != "quick" || got.RequestSummarySource.Text != summary.Text {
					t.Fatalf("stored summary %+v", got.RequestSummarySource)
				}
			}
		})
	}
}

type requestWriter struct {
	calls int
	fail  bool
	t     *testing.T
}

func (w *requestWriter) Ask(_ context.Context, call modelcall.Call) (string, *modelcall.Usage, error) {
	if call.Purpose == "request-summary" {
		w.calls++
		var messages []RequestMessage
		if err := json.Unmarshal([]byte(call.User), &messages); err != nil || len(messages) < 2 || messages[1].Text != "Also keep keyboard controls." {
			w.t.Fatalf("request inputs: %s", call.User)
		}
		if call.Model != "quick" {
			w.t.Fatalf("model %q", call.Model)
		}
		if w.fail {
			return "", nil, errors.New("provider unavailable")
		}
		return `{"title":"Bring distant cards into view","summary":"Make distant cards easy to reach while keeping keyboard controls."}`, nil, nil
	}
	return "The cards are easier to reach.", nil, nil
}

func TestRequestSummaryReusesMessagesAndRetainsModelAndSource(t *testing.T) {
	t.Parallel()
	owner, source := narrativeFixture(t)
	db := owner.db
	work := newService(db, allowProject(source.Change.ProjectID), Runtime{})
	t.Cleanup(work.Stop)
	messages := []RequestMessage{{Session: "chat", Index: 1, Text: "Ok so a card is way down the board; bring it into view."}, {Session: "chat", Index: 3, Text: "Also keep keyboard controls."}}
	writer := &requestWriter{t: t}
	resolve := func(context.Context, ids.ID) (string, string, error) { return "quick", "main-fast", nil }
	ctx := context.Background()
	var err error
	for range 2 {
		source, err = work.NarrativeSource(ctx, source.Change.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := work.SummarizeRequest(ctx, source, messages, resolve, writer); err != nil {
			t.Fatal(err)
		}
	}
	ch, err := work.Change(ctx, source.Change.ID)
	if err != nil || ch.RequestSummarySource == nil {
		t.Fatalf("summary: %+v %v", ch, err)
	}
	if writer.calls != 1 || ch.Title != "Bring distant cards into view" || ch.RequestSummarySource.Model != "quick" || ch.RequestSummarySource.Slot != "main-fast" || len(ch.RequestSummarySource.Messages) != 2 || ch.RequestSummarySource.Messages[1] != messages[1] {
		t.Fatalf("calls=%d change=%+v request=%+v", writer.calls, ch, ch.RequestSummarySource)
	}
	public, err := json.Marshal(ch)
	if err != nil || strings.Contains(string(public), messages[0].Text) || !strings.Contains(string(public), `"requestSummary":"Make distant cards easy to reach while keeping keyboard controls."`) {
		t.Fatalf("change projection exposed request inputs or omitted its summary: %s %v", public, err)
	}
	receipts, err := audit.Recent(ctx, db, audit.Filter{KindPrefix: "change.request-summarized"})
	if err != nil || len(receipts) != 1 {
		t.Fatalf("receipts %+v %v", receipts, err)
	}
	messages = append(messages, RequestMessage{Session: "chat", Index: 5, Text: "Keep touch controls too."})
	source, err = work.NarrativeSource(ctx, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := work.SummarizeRequest(ctx, source, messages, resolve, writer); err != nil {
		t.Fatal(err)
	}
	ch, err = work.Change(ctx, ch.ID)
	if err != nil || writer.calls != 2 || len(ch.RequestSummarySource.Messages) != 3 {
		t.Fatalf("new request: %d %+v %v", writer.calls, ch, err)
	}
}

func TestRequestSummaryFailureRetainsFirstMessageAndHumanPin(t *testing.T) {
	t.Parallel()
	for _, pin := range []bool{false, true} {
		t.Run(map[bool]string{false: "first message", true: "human pin"}[pin], func(t *testing.T) {
			owner, source := narrativeFixture(t)
			work := newService(owner.db, allowProject(source.Change.ProjectID), Runtime{})
			t.Cleanup(work.Stop)
			ctx := context.Background()
			wantTitle := source.Change.Title
			if pin {
				wantTitle = "My chosen title"
				rev := source.Change.EditRevision
				if _, err := work.Edit(ctx, ChangeEdit{ID: source.Change.ID, ProjectID: source.Change.ProjectID, Title: &wantTitle, ExpectedRevision: &rev}); err != nil {
					t.Fatal(err)
				}
			}
			writer := &requestWriter{t: t, fail: !pin}
			messages := []RequestMessage{{Text: "Bring distant cards into view."}, {Text: "Also keep keyboard controls."}}
			err := work.SummarizeRequest(ctx, source, messages, func(context.Context, ids.ID) (string, string, error) { return "quick", "fast", nil }, writer)
			if (err != nil) != !pin {
				t.Fatalf("summary failure: %v", err)
			}
			ch, err := work.Change(ctx, source.Change.ID)
			if err != nil || ch.Title != wantTitle || ch.Body != "brief" || (ch.RequestSummarySource == nil) != !pin {
				t.Fatalf("change %+v %v", ch, err)
			}
		})
	}
}
