package httpapi

import (
	"net/http"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/repositoryedit"
	"flokkit.io/forge/internal/worker"
)

type ConsoleSessions struct {
	sessions *console.Service
	chats    conversation.Chats
	cli      ConsoleCLI
}

func NewConsoleSessions(chats conversation.Chats, cli ConsoleCLI) ConsoleSessions {
	return ConsoleSessions{sessions: chats.Sessions, chats: chats, cli: cli}
}

// ConsoleList lists a project's sessions with ladder state.
// GET /projects/{slug}/console
func (h ConsoleSessions) ConsoleList(w http.ResponseWriter, r *http.Request) {
	out := []ConsoleSessionView{}
	for _, s := range h.sessions.List(r.Context(), console.Match{Labels: map[string]string{conversation.SlugLabel: r.PathValue("slug")}}) {
		out = append(out, h.View(r, s))
	}
	WriteJSON(w, 200, ConsoleSessionList{Sessions: out})
}

// ConsoleCLI supplies the lazily built agent executable, with one process-local cache.
type ConsoleCLI interface{ Path() (string, error) }

func (h ConsoleSessions) View(r *http.Request, s *console.Session) ConsoleSessionView {
	v := ConsoleSessionView{
		ID: s.ID, ProjectSlug: conversation.Of(s).Slug, Model: s.Model,
		CreatedAt: s.CreatedAt, State: h.sessions.State(r.Context(), s), ChangeID: conversation.Of(s).ChangeID,
		ContinuedFrom: s.ResumedFrom, Turn: s.Activity(),
		OpenedBy: s.Actor, MayAct: h.chats.MayAct(r.Context(), s),
	}
	// A session whose container has no AppTown CLI is degraded (it cannot
	// preview, invoke, or complain): say so instead of degrading quietly.
	if _, err := h.cli.Path(); err != nil {
		v.CLIMissing = true
	}
	// A chat on the worker says so, with its last save and how fresh the
	// forge's copy of its conversation is.
	if conversation.OnWorkerRecord(s.Meta) {
		v.Placement = conversation.PlaceOnWorker
		v.Saved = ConsoleSaveOf(repositoryedit.LastChatSave(s.Dir))
		if at := worker.RecordAsOf(s.Dir); !at.IsZero() {
			v.RecordAsOf = &at
		}
	}
	return v
}

// ConsoleSessionView is a chat session as the pages list it.
type ConsoleSessionView struct {
	ID            string        `json:"id"`
	ProjectSlug   string        `json:"projectSlug"`
	Model         string        `json:"model"`
	CreatedAt     time.Time     `json:"createdAt"`
	State         console.State `json:"state"`
	ChangeID      ids.ID        `json:"changeId"`
	ContinuedFrom string        `json:"continuedFrom,omitempty"`
	// OpenedBy is the person who opened the chat; it runs with their authority.
	OpenedBy string `json:"openedBy"`
	// MayAct says whether the caller may act on the chat (say, paste, stop,
	// type, end) or only look. Presentation: each act is checked itself.
	MayAct     bool `json:"mayAct"`
	CLIMissing bool `json:"cliMissing,omitempty"`
	// Turn is "working" while the agent has the turn, "handed-back" once it
	// answered, and empty before anyone has spoken in the chat.
	Turn console.Turn `json:"turn,omitempty"`
	// Placement is "worker" for a chat on the worker; empty on AppTown.
	Placement string `json:"placement,omitempty"`
	// Saved is a worker chat's last save; absent before its first.
	Saved *ConsoleSave `json:"saved,omitempty"`
	// RecordAsOf is when AppTown last copied a worker chat's conversation.
	RecordAsOf *time.Time `json:"recordAsOf,omitempty"`
}

// ConsoleSave is a worker chat's last save: saved, refused (Kept: its commit
// is kept for download), not-confirmed, skipped, or unknown (its record could
// not be read).
type ConsoleSave struct {
	At      time.Time `json:"at"`
	Outcome string    `json:"outcome"`
	// Commit is the branch's saved commit after this save.
	Commit string `json:"commit,omitempty"`
	Reason string `json:"reason,omitempty"`
	Kept   bool   `json:"kept,omitempty"`
}

// ConsoleSessionList is a project's chat sessions.
type ConsoleSessionList struct {
	Sessions []ConsoleSessionView `json:"sessions"`
}
