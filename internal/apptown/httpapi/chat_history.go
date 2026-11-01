package httpapi

import (
	"net/http"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/ids"
)

// ChatHistory serves the words of a change's or a project's chats, read
// from their records on disk: sessions end and their runtimes go, the words
// stay. It reads; nothing here wakes or acts on a chat.
type ChatHistory struct {
	sessions *console.Service
	projects *project.Projects
	access   *identity.Service
	dataDir  string
	// mask replaces known secret values in what a chat's history shows; the
	// record files keep what the agent saw.
	mask mask
}

func NewChatHistory(sessions *console.Service, projects *project.Projects, access *identity.Service, dataDir string, mask mask) ChatHistory {
	return ChatHistory{sessions: sessions, projects: projects, access: access, dataDir: dataDir, mask: mask}
}

type (
	// ChatHistoryView is a change's or a project's chats, oldest first.
	ChatHistoryView struct {
		Sessions []ChatHistoryEntry `json:"sessions"`
		// Unreadable is how many chat records on this AppTown could not be
		// read: any of them may belong here, so some chats may be missing.
		Unreadable int `json:"unreadable"`
		// UnreadableFolders names them, for an administrator to look at.
		UnreadableFolders []string `json:"unreadableFolders,omitempty"`
	}
	// ChatHistoryEntry is one chat: who opened it, when, whether it has
	// ended, whether it was moved here from another AppTown, and its words.
	ChatHistoryEntry struct {
		ID        string            `json:"id"`
		CreatedAt time.Time         `json:"createdAt"`
		Actor     string            `json:"actor"`
		Ended     bool              `json:"ended"`
		Imported  bool              `json:"imported"`
		Messages  []console.Message `json:"messages"`
	}
)

// ChangeTranscript: GET /changes/{id}/transcript — every chat this change
// has had, live or ended; the words belong to the change's story.
func (h ChatHistory) ChangeTranscript(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	h.write(w, r, conversation.Key(ids.Nil, id), false)
}

// ProjectTranscript: GET /projects/{slug}/transcript — the ended chats
// opened on the project itself (not on a change): its past chats. Live ones
// are on the project's chat list.
func (h ChatHistory) ProjectTranscript(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	h.write(w, r, conversation.Key(p.ID, ids.Nil), true)
}

func (h ChatHistory) write(w http.ResponseWriter, r *http.Request, scope string, endedOnly bool) {
	convs, unreadable, err := h.sessions.Transcript(r.Context(), scope)
	if err != nil {
		WriteFault(w, err)
		return
	}
	out := ChatHistoryView{Sessions: []ChatHistoryEntry{}, Unreadable: len(unreadable)}
	if len(unreadable) > 0 && isAdmin(h.access, r) {
		out.UnreadableFolders = unreadable
	}
	for _, c := range convs {
		if endedOnly && !c.Ended {
			continue
		}
		out.Sessions = append(out.Sessions, ChatHistoryEntry{ID: c.ID, CreatedAt: c.CreatedAt, Actor: c.Actor,
			Ended: c.Ended, Imported: conversation.Imported(c.Labels), Messages: h.mask.messages(r.Context(), c.Messages)})
	}
	WriteJSON(w, 200, out)
}

// ChangePicture: GET /changes/{id}/transcript/{chat}/pictures/{name} — a
// picture pasted into one of the change's chats, live or ended. The chat is
// named by its id and the picture by the name the chat's words give it;
// neither is a path, and nothing is listed.
func (h ChatHistory) ChangePicture(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	h.picture(w, r, conversation.Key(ids.Nil, id))
}

// ProjectPicture: GET /projects/{slug}/transcript/{chat}/pictures/{name} —
// a picture pasted into one of the chats opened on the project.
func (h ChatHistory) ProjectPicture(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	h.picture(w, r, conversation.Key(p.ID, ids.Nil))
}

func (h ChatHistory) picture(w http.ResponseWriter, r *http.Request, scope string) {
	path, err := h.sessions.RecordedPicture(scope, r.PathValue("chat"), r.PathValue("name"))
	if err != nil {
		WriteError(w, 404, "no such picture")
		return
	}
	ServeUntrustedFile(h.dataDir, w, r, path)
}
