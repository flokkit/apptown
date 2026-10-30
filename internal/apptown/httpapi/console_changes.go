package httpapi

import (
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/status"
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/database"
)

type ConsoleChanges struct {
	sessions *console.Service
	work     *workflow.Service
	actions  *changeactions.Service
	db       *database.DB
}

func NewConsoleChanges(sessions *console.Service, work *workflow.Service, actions *changeactions.Service, db *database.DB) ConsoleChanges {
	return ConsoleChanges{sessions: sessions, work: work, actions: actions, db: db}
}

// ConsoleChanges: GET /console/{sid}/changes — the live state of every
// change this conversation started, in plain words.
func (h ConsoleChanges) ConsoleChanges(w http.ResponseWriter, r *http.Request) {
	s := ConsoleFromPath(h.sessions, w, r)
	if s == nil {
		return
	}
	if conversation.Of(s).ProjectID.IsZero() {
		WriteError(w, 409, "session has no project scope")
		return
	}
	idList := conversation.Started(s)
	out := []ConsoleChange{}
	var runIDs []ids.ID
	records, err := h.work.ChangeRecords(r.Context(), conversation.Of(s).ProjectID, idList)
	if err != nil {
		WriteFault(w, err)
		return
	}
	byID := make(map[ids.ID]*workflow.Change, len(records))
	for _, ch := range records {
		byID[ch.ID] = ch
	}
	for _, id := range idList {
		ch := byID[id]
		if ch == nil {
			continue
		}
		out = append(out, ConsoleChange{ChangeID: id, Title: ch.Title, State: ch.State, ClosedAs: ch.ClosedAs, StateLabel: ch.StateLabel, StatusLabel: ch.Display(nil), runID: ch.RunID})
		if !ch.RunID.IsZero() {
			runIDs = append(runIDs, ch.RunID)
		}
	}
	activities, err := h.work.Activities(r.Context(), runIDs)
	if err != nil {
		WriteFault(w, err)
		return
	}
	for i := range out {
		if activity, ok := activities[out[i].runID]; ok && activity.Active() {
			out[i].Doing, out[i].Detail = string(activity.State), activity.Detail
			out[i].StatusLabel = activity.State.Display()
		}
	}
	WriteJSON(w, 200, ConsoleChangeList{Changes: out})
}

// ConsoleLand: POST /console/{sid}/land — start the landing of the
// chat's OWN change. Starting is not shipping: the land run's gates and its
// human approval still stand between this call and main.
func (h ConsoleChanges) ConsoleLand(w http.ResponseWriter, r *http.Request) {
	s := consoleForTokenRequest(h.sessions, w, r)
	if s == nil {
		return
	}
	if conversation.Of(s).ChangeID.IsZero() {
		WriteError(w, 403, "only a change chat can land — this chat has no change attached")
		return
	}
	ctx := conversation.Context(r.Context(), s)
	ch, err := h.work.Change(ctx, conversation.Of(s).ChangeID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	if ch.State != workflow.ChangeInProgress {
		WriteError(w, 409, "change #%d is %s — only in-progress changes land", ch.ID, ch.State)
		return
	}
	p, err := project.ByIDIn(ctx, h.db, ch.ProjectID)
	if err != nil {
		writeReadFault(w, err, "no such project")
		return
	}
	run, err := h.actions.RequestLanding(ctx, &p, ch, s.Actor+" via console/"+s.ID)
	if err != nil {
		WriteError(w, 422, "%v", err)
		return
	}
	WriteJSON(w, 201, LandingStarted{RunID: run.ID, Message: "landing started — AppTown runs its checks, then asks a person for the final OK"})
}

// ConsoleRecheck: POST /console/{sid}/recheck — after fixing what a
// landing gate rejected, re-run the FAILED automatic gates of the chat's own
// landing run. It never touches approvals.
func (h ConsoleChanges) ConsoleRecheck(w http.ResponseWriter, r *http.Request) {
	s := consoleForTokenRequest(h.sessions, w, r)
	if s == nil {
		return
	}
	if conversation.Of(s).ChangeID.IsZero() {
		WriteError(w, 403, "only a change chat can recheck — this chat has no change attached")
		return
	}
	if _, err := h.work.Change(conversation.Context(r.Context(), s), conversation.Of(s).ChangeID); err != nil {
		WriteFault(w, err)
		return
	}
	rechecked, err := h.work.RecheckLanding(conversation.Context(r.Context(), s), workflow.LandingRecheck{ChangeID: conversation.Of(s).ChangeID})
	if err != nil {
		WriteError(w, 422, "%v", err)
		return
	}
	WriteJSON(w, 200, recheckStarted(rechecked))
}

// recheckStarted is a landing recheck's answer, from a chat or the change.
func recheckStarted(rechecked workflow.LandingRechecked) RecheckStarted {
	msg := "nothing to recheck — no failed gate on this change's landing"
	if len(rechecked.Names) > 0 {
		msg = fmt.Sprintf("re-running %d failed gate(s): %s", len(rechecked.Failed), strings.Join(rechecked.Failed, ", "))
		if len(rechecked.Names) > len(rechecked.Failed) {
			msg += " (after catching up with main again)"
		}
		msg += " — watch with apptown status"
	}
	return RecheckStarted{RunID: rechecked.RunID, Rerun: rechecked.Names, Message: msg}
}

// The chat's own-change responses.
type (
	// ConsoleChange is a Change a chat started, with what it is doing now.
	ConsoleChange struct {
		runID    ids.ID
		ChangeID ids.ID               `json:"changeId"`
		Title    string               `json:"title"`
		State    workflow.ChangeState `json:"state"`
		ClosedAs string               `json:"closedAs,omitempty"` // as workflow.Change.ClosedAs
		// StateLabel is workflow.Change.StateLabel.
		StateLabel  string               `json:"stateLabel"`
		StatusLabel status.DisplayStatus `json:"statusLabel"`
		Doing       string               `json:"doing"`
		Detail      string               `json:"detail"`
	}
	ConsoleChangeList struct {
		Changes []ConsoleChange `json:"changes"`
	}
	// LandingStarted names the landing run a chat started.
	LandingStarted struct {
		RunID   ids.ID `json:"runId"`
		Message string `json:"message"`
	}
	// RecheckStarted names the failed gates being run again.
	RecheckStarted struct {
		// RunID is the landing whose gates run again.
		RunID   ids.ID   `json:"runId"`
		Rerun   []string `json:"rerun"`
		Message string   `json:"message"`
	}
)
