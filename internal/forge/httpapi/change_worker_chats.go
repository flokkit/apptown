package httpapi

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/changeconsole"
	"flokkit.io/forge/internal/forge/repositoryedit"
	"flokkit.io/forge/internal/forge/workflow"
)

// WorkerChats are a change's chats on the worker, live and ended, with
// where their work stands (design/operations/workers.md, "Chats on the
// worker").
// The owner is changeconsole.Service.
type WorkerChats interface {
	WorkerChats(ctx context.Context, ch *workflow.Change) ([]changeconsole.WorkerChat, error)
	// RefusedBundle writes an ended chat's refused last save as a Git bundle.
	RefusedBundle(ctx context.Context, ch *workflow.Change, chat string, w io.Writer) error
}

// WorkerChatView is one of a change's chats on the worker.
type WorkerChatView struct {
	Chat string `json:"chat"`
	// Live: the chat has not ended.
	Live bool `json:"live"`
	// Saved is its last save; absent before its first.
	Saved *ConsoleSave `json:"saved,omitempty"`
	// RecordAsOf is when AppTown last copied its conversation.
	RecordAsOf *time.Time `json:"recordAsOf,omitempty"`
	// Refused is an ended chat's refused last save, kept for its download.
	Refused *RefusedLastSave `json:"refused,omitempty"`
}

// RefusedLastSave is the commit an ended chat's last save could not put on
// its change, and why.
type RefusedLastSave struct {
	Commit string    `json:"commit"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// WorkerChatList is a change's chats on the worker.
type WorkerChatList struct {
	Chats []WorkerChatView `json:"chats"`
}

// workerChatView is a chat on the worker as the change's page shows it.
func workerChatView(c changeconsole.WorkerChat) WorkerChatView {
	v := WorkerChatView{Chat: c.ID, Live: c.Live, Saved: ConsoleSaveOf(c.LastSave, c.HasSave, c.SaveErr)}
	if !c.RecordAsOf.IsZero() {
		at := c.RecordAsOf
		v.RecordAsOf = &at
	}
	if c.Refused != "" {
		v.Refused = &RefusedLastSave{Commit: c.Refused, Reason: c.LastSave.Reason, At: c.LastSave.At}
	}
	return v
}

// workerChatViews is a change's chats on the worker as its page shows them.
func (h ChangeWorkerChats) workerChatViews(ctx context.Context, ch *workflow.Change) ([]WorkerChatView, error) {
	chats, err := h.chats.WorkerChats(ctx, ch)
	if err != nil {
		return nil, err
	}
	out := make([]WorkerChatView, 0, len(chats))
	for _, c := range chats {
		out = append(out, workerChatView(c))
	}
	return out, nil
}

// ConsoleSaveOf is a chat's last save as the pages show it: an unreadable
// record is "unknown", never no save.
func ConsoleSaveOf(save repositoryedit.ChatSave, ok bool, err error) *ConsoleSave {
	switch {
	case err != nil:
		return &ConsoleSave{Outcome: "unknown", Reason: "its last save could not be read: " + err.Error()}
	case !ok:
		return nil
	}
	return &ConsoleSave{At: save.At, Outcome: save.Outcome, Commit: save.Saved, Reason: save.Reason, Kept: save.Kept != ""}
}

type ChangeWorkerChats struct {
	db    database.Queryer
	chats WorkerChats
}

func NewChangeWorkerChats(db database.Queryer, chats WorkerChats) ChangeWorkerChats {
	return ChangeWorkerChats{db: db, chats: chats}
}

// List: GET /api/v1/changes/{id}/worker-chats.
func (h ChangeWorkerChats) List(w http.ResponseWriter, r *http.Request) {
	ch, ok := h.change(w, r)
	if !ok {
		return
	}
	chats, err := h.workerChatViews(r.Context(), ch)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, WorkerChatList{Chats: chats})
}

var chatID = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// RefusedBundle: GET /api/v1/changes/{id}/worker-chats/{chat}/refused, a Git
// bundle of an ended chat's refused last save: fetch or clone it to repair
// the commits.
func (h ChangeWorkerChats) RefusedBundle(w http.ResponseWriter, r *http.Request) {
	ch, ok := h.change(w, r)
	if !ok {
		return
	}
	chat := r.PathValue("chat")
	if !chatID.MatchString(chat) {
		WriteError(w, 400, "bad chat id")
		return
	}
	chats, err := h.workerChatViews(r.Context(), ch)
	if err != nil {
		WriteFault(w, err)
		return
	}
	found := false
	for _, c := range chats {
		found = found || (c.Chat == chat && c.Refused != nil)
	}
	if !found {
		WriteError(w, 404, "chat %s of change %d left no refused last save", chat, ch.ID)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="change-`+r.PathValue("id")+`-chat-`+chat+`.bundle"`)
	if err := h.chats.RefusedBundle(r.Context(), ch, chat, w); err != nil {
		WriteFault(w, err)
	}
}

func (h ChangeWorkerChats) change(w http.ResponseWriter, r *http.Request) (*workflow.Change, bool) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return nil, false
	}
	ch, err := workflow.ReadChangeIn(r.Context(), h.db, id)
	if err != nil {
		writeReadFault(w, err, "no such change #%d", id)
		return nil, false
	}
	return ch, true
}
