package httpapi

import (
	"log"
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/database"
)

type ConsoleMessages struct {
	sessions *console.Service
	chats    conversation.Chats
	work     *workflow.Service
	db       *database.DB
	dataDir  string
	mask     mask // known secret values in what the chat view shows
}

// NewConsoleMessages serves a chat's conversation to anyone who may look at
// it, and takes words, files and interruptions through chats, which
// decides who may act on it. work names a change from its chat's first
// message.
func NewConsoleMessages(chats conversation.Chats, work *workflow.Service, db *database.DB, dataDir string, mask mask) ConsoleMessages {
	return ConsoleMessages{sessions: chats.Sessions, chats: chats, work: work, db: db, dataDir: dataDir, mask: mask}
}

// ConsoleMessages: GET /console/{sid}/messages?from=N — the parsed
// conversation. An open chat page keeps the session warm for someone who
// may act on it (conversation.Chats.Messages).
func (h ConsoleMessages) ConsoleMessages(w http.ResponseWriter, r *http.Request) {
	s := ConsoleFromPath(h.sessions, w, r)
	if s == nil {
		return
	}
	from, ok := queryInt(w, r, "from", 0)
	if !ok {
		return
	}
	page, err := h.chats.Messages(r.Context(), s, from)
	if err != nil {
		WriteError(w, 404, "no such session")
		return
	}
	WriteJSON(w, 200, ConsoleMessagePage{Messages: h.mask.messages(r.Context(), page.Messages), State: h.sessions.State(r.Context(), s), Turn: string(page.Turn)})
}

// ConsoleSay: POST /console/{sid}/say {text} — typed words go into the
// SAME pi the terminal shows. One conversation, two zooms.
func (h ConsoleMessages) ConsoleSay(w http.ResponseWriter, r *http.Request) {
	s := ConsoleFromPath(h.sessions, w, r)
	if s == nil {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "say something"}, func(in struct {
		Text        string   `json:"text"`
		Attachments []string `json:"attachments"`
	}) {
		if strings.TrimSpace(in.Text) == "" && len(in.Attachments) == 0 {
			WriteError(w, 400, "say something")
			return
		}

		actor, err := h.chats.SayWithAttachments(r.Context(), s, in.Text, in.Attachments)
		if err != nil {
			WriteFault(w, err)
			return
		}
		// Delivery already succeeded. Optional naming must not ask the person to
		// resend their message; only a still-awaiting title can be claimed.
		if !conversation.Of(s).ChangeID.IsZero() {
			text := strings.Join(strings.Fields(strings.ReplaceAll(in.Text, "\n", " ")), " ")
			if _, err := h.work.NameFromChat(r.Context(), conversation.Of(s).ProjectID, conversation.Of(s).ChangeID, text, actor); err != nil {
				log.Printf("change %d: first chat title: %v", conversation.Of(s).ChangeID, err)
			}
		}
		WriteJSON(w, 200, Done{OK: true})
	})
}

// ConsoleInterrupt: POST /console/{sid}/interrupt — press Escape in
// the helper's terminal: pi aborts the current turn and hands control back.
func (h ConsoleMessages) ConsoleInterrupt(w http.ResponseWriter, r *http.Request) {
	s := ConsoleFromPath(h.sessions, w, r)
	if s == nil {
		return
	}
	if err := h.chats.Interrupt(r.Context(), s); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}

func (h ConsoleMessages) ConsoleUpload(w http.ResponseWriter, r *http.Request) {
	s := ConsoleFromPath(h.sessions, w, r)
	if s == nil {
		return
	}
	if _, err := h.chats.Act(r.Context(), s, "attaching a file to"); err != nil {
		WriteFault(w, err)
		return
	}
	multipartFileCommand(w, r, func(file console.File) {
		att, err := h.chats.Upload(r.Context(), s, file)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, att)
	})
}

// ConsoleUploadGet serves a file back to the chat page.
func (h ConsoleMessages) ConsoleUploadGet(w http.ResponseWriter, r *http.Request) {
	s := ConsoleFromPath(h.sessions, w, r)
	if s == nil {
		return
	}
	name := r.PathValue("name")
	if !console.ValidAttachmentName(name) {
		WriteError(w, 404, "no such file")
		return
	}
	path, err := h.sessions.UploadPath(r.Context(), s.ID, name)
	if err != nil {
		WriteError(w, 404, "no such session")
		return
	}
	ServeUntrustedFile(h.dataDir, w, r, path)
}

// The chat routes' responses.
type (
	// ConsoleMessagePage is a page of a chat's messages and where the turn is.
	ConsoleMessagePage struct {
		Messages []console.Message `json:"messages"`
		State    console.State     `json:"state"`
		Turn     string            `json:"turn"`
	}
)
