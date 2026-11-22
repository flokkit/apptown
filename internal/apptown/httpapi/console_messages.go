package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
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

// ConsoleStream: GET /console/{sid}/stream?after=<cursor> — the chat's
// conversation as it changes, as Server-Sent Events, for the chat page
// (conversation.Chats.Follow). Each event's data is a ConsoleStreamUpdate:
// the messages from `from` on, the message the helper is writing, whose
// turn it is, and the cursor to resume from. A refusal before the first
// update is an ordinary refusal; one after it is a "refused" event naming
// the status the read would now get, and the stream ends. A comment every 15 s keeps idle proxies from closing it; a
// proxy must stream, not buffer, the response.
func (h ConsoleMessages) ConsoleStream(w http.ResponseWriter, r *http.Request) {
	s := ConsoleFromPath(h.sessions, w, r)
	if s == nil {
		return
	}
	ctx, stop := context.WithCancel(r.Context())
	// The keep-alive writes to w too: it ends before the handler returns.
	var alive sync.WaitGroup
	defer alive.Wait()
	defer stop()
	rc := http.NewResponseController(w)
	var (
		mu      sync.Mutex
		started bool
		masked  func(string) string
	)
	write := func(event string) error {
		mu.Lock()
		defer mu.Unlock()
		if _, err := io.WriteString(w, event); err != nil {
			return err
		}
		return rc.Flush()
	}
	err := h.chats.Follow(ctx, s, console.Cursor(r.URL.Query().Get("after")), func(u console.Update) error {
		// One snapshot of the secrets to mask when the stream opens and
		// when messages arrive, never for each draft: it may ask Infisical.
		if masked == nil || len(u.Messages) > 0 {
			masked = h.mask.snapshot(ctx)
		}
		raw, err := json.Marshal(ConsoleStreamUpdate{From: u.From, Messages: maskMessages(masked, u.Messages),
			Draft: maskDraft(masked, u.Draft), Turn: string(u.Turn), Cursor: string(u.Cursor)})
		if err != nil {
			return err
		}
		if !started {
			started = true
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			alive.Add(1)
			go func() { defer alive.Done(); keepAlive(ctx, write) }()
		}
		return write("data: " + string(raw) + "\n\n")
	})
	switch {
	case !started && err != nil:
		WriteFaultOr(w, err, http.StatusInternalServerError)
	case started && err != nil && ctx.Err() == nil:
		// What the reader would have been answered had it asked now: 403
		// when its read was taken away, 404 when the chat went.
		raw, _ := json.Marshal(map[string]any{"error": fault.Message(err), "status": faultStatus(fault.KindOf(err), http.StatusInternalServerError)})
		_ = write("event: refused\ndata: " + string(raw) + "\n\n")
	}
}

// keepAlive writes a comment every 15 s until ctx ends.
func keepAlive(ctx context.Context, write func(string) error) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if write(": still here\n\n") != nil {
				return
			}
		}
	}
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
	// ConsoleStreamUpdate is one event of a chat's stream: the messages
	// from From on replace what the reader holds from there; Draft is the
	// message the helper is writing, whole, or null; Cursor is what the
	// reader holds after it, sent back as ?after= to resume.
	ConsoleStreamUpdate struct {
		From     int               `json:"from"`
		Messages []console.Message `json:"messages"`
		Draft    *console.Message  `json:"draft"`
		Turn     string            `json:"turn"`
		Cursor   string            `json:"cursor"`
	}
)
