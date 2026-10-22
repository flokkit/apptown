package httpapi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
)

func TestChangeDetailExplainsDraftsAndRecordedAbandonment(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"ordinary", "chat", "proposal", "closed draft", "closed run", "stopped run"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			withRun := kind == "closed run" || kind == "stopped run"
			f := newWorkflowHTTPFixture(t, withRun)
			id := f.ChangeID
			if kind == "chat" || kind == "proposal" {
				ch, err := f.Work.Create(f.Context, workflow.ChangeDraft{ProjectID: f.ProjectID, Title: "another draft", TitleFromChat: kind == "chat", Propose: kind == "proposal"})
				if err != nil {
					t.Fatal(err)
				}
				id = ch.ID
			}
			want, state := "No workflow has started yet.", ""
			if kind == "proposal" {
				want = ""
			}
			if kind == "closed draft" || withRun {
				want, state = "duplicate of #297; original invoke never started", "abandoned"
				stop := workflow.StopWork{ChangeID: id, Reason: want}
				if kind == "stopped run" {
					stop.ChangeID, stop.RunID = ids.Nil, f.RunID
				}
				if _, err := f.Work.Abandon(f.Context, stop); err != nil {
					t.Fatal(err)
				}
			}
			w := workflowChangeRead(t, f, fmt.Sprint(id))
			var got struct {
				State, StateDetail string
				Detail             *struct{ State, StateDetail string }
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
				t.Fatalf("detail: %d %s %v", w.Code, w.Body.String(), err)
			}
			if got.State != state || got.StateDetail != want {
				t.Fatalf("explanation: %+v; want %q / %q", got, state, want)
			}
			if withRun && (got.Detail == nil || got.Detail.State != state || got.Detail.StateDetail != want) {
				t.Fatalf("run explanation: %+v", got.Detail)
			}
		})
	}
}
