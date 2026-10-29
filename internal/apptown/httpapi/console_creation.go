package httpapi

import (
	"context"
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/console"
)

type ConsoleCreation struct {
	projects *project.Projects
	access   *identity.Service
	opener   ConsoleOpener
	view     ConsoleSessions
	authMode string
}

func NewConsoleCreation(projects *project.Projects, access *identity.Service, opener ConsoleOpener, view ConsoleSessions, authMode string) ConsoleCreation {
	return ConsoleCreation{projects: projects, access: access, opener: opener, view: view, authMode: authMode}
}

type ConsoleOpener interface {
	Open(context.Context, *project.Project, conversation.ProjectOpen, string) (*console.Session, error)
}

func (h ConsoleCreation) ConsoleCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	if !RequireProjectMember(h.access, w, r, p.ID, p.Slug) {
		return
	}
	jsonCommand(w, r, jsonBody{optional: true}, func(in conversation.ProjectOpen) {
		s, err := h.opener.Open(r.Context(), p, in, Actor(r))
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 201, ConsoleOpened{Session: h.view.View(r, s)})
	})
}

// ConsoleOpened is the chat session just opened.
type ConsoleOpened struct {
	Session ConsoleSessionView `json:"session"`
}
