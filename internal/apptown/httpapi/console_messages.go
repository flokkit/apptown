package httpapi

import (
	"encoding/base64"
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
	mask     Mask // known secret values in what the chat view shows
}

// NewConsoleMessages serves a chat's conversation to anyone who may look at
// it, and takes words, pictures and interruptions through chats, which
// decides who may act on it. work names a change from its chat's first
// message.
func NewConsoleMessages(chats conversation.Chats, work *workflow.Service, db *database.DB, dataDir string, mask Mask) ConsoleMessages {
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
	from, ok := QueryInt(w, r, "from", 0)
	if !ok {
		return
	}
	page, err := h.chats.Messages(r.Context(), s, from)
	if err != nil {
		WriteError(w, 404, "no such session")
		return
	}
	WriteJSON(w, 200, ConsoleMessagePage{Messages: h.mask.Messages(r.Context(), page.Messages), State: h.sessions.State(r.Context(), s), Turn: string(page.Turn)})
}

// ConsoleSay: POST /console/{sid}/say {text} — typed words go into the
// SAME pi the terminal shows. One conversation, two zooms.
func (h ConsoleMessages) ConsoleSay(w http.ResponseWriter, r *http.Request) {
	s := ConsoleFromPath(h.sessions, w, r)
	if s == nil {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "say something"}, func(in struct {
		Text string `json:"text"`
	}) {
		if strings.TrimSpace(in.Text) == "" {
			WriteError(w, 400, "say something")
			return
		}

		actor, err := h.chats.Say(r.Context(), s, in.Text)
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
	jsonCommand(w, r, jsonBody{limit: 20 << 20, errorFormat: "bad upload: %v"}, func(in struct {
		Data string `json:"data"` // data: URL from the browser clipboard
	}) {
		ext := ""
		for prefix, e := range map[string]string{
			"data:image/png;base64,":  "png",
			"data:image/jpeg;base64,": "jpg",
			"data:image/webp;base64,": "webp",
			"data:image/gif;base64,":  "gif",
		} {
			if strings.HasPrefix(in.Data, prefix) {
				in.Data, ext = in.Data[len(prefix):], e
				break
			}
		}
		if ext == "" {
			WriteError(w, 422, "only pictures can be pasted (png, jpeg, webp, gif)")
			return
		}
		raw, err := base64.StdEncoding.DecodeString(in.Data)
		if err != nil {
			WriteError(w, 400, "the picture data didn't decode")
			return
		}
		if len(raw) > 12<<20 {
			WriteError(w, 413, "picture too big — 12MB is the cap")
			return
		}
		att, err := h.chats.Upload(r.Context(), s, console.File{Ext: ext, Body: raw})
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, PastedPicture{Name: att.Name, Path: att.Path})
	})
}

// ConsoleUploadGet serves a pasted picture back to the chat page.
func (h ConsoleMessages) ConsoleUploadGet(w http.ResponseWriter, r *http.Request) {
	s := ConsoleFromPath(h.sessions, w, r)
	if s == nil {
		return
	}
	name := r.PathValue("name")
	if !console.ValidPictureName(name) {
		WriteError(w, 404, "no such picture")
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
	// PastedPicture names a picture pasted into a chat.
	PastedPicture struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
)
