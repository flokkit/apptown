package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/gitlab"
	"github.com/flokkit/apptown/internal/strs"
)

type SCMRemotes struct {
	projects    *project.Projects
	provider    repositoryProvider
	attachments *scm.RepositoryAttachments
}

func NewSCMRemotes(projects *project.Projects, provider repositoryProvider, attachments *scm.RepositoryAttachments) SCMRemotes {
	return SCMRemotes{projects: projects, provider: provider, attachments: attachments}
}

// SCMNamespaces: GET /api/v1/scm/namespaces — the groups forge-bot can
// create projects in, so the wizard can show names instead of raw ids. A
// AppTown with no GitLab connection answers configured: false with no groups:
// keeping code only on AppTown is a normal setup, not a failure, and the
// wizard then offers no GitLab copy. A connection that is set up but cannot
// be reached is still an error.
func (h SCMRemotes) SCMNamespaces(w http.ResponseWriter, r *http.Request) {
	gl, err := h.provider.Client(r.Context())
	if errors.Is(err, gitlab.ErrNotConfigured) {
		WriteJSON(w, 200, RemoteNamespaces{Namespaces: []RemoteNamespace{}, Configured: false})
		return
	}
	if err != nil {
		WriteError(w, 503, "%s", err.Error())
		return
	}
	var groups []struct {
		ID       int64  `json:"id"`
		FullPath string `json:"full_path"`
		Name     string `json:"name"`
	}
	if err := gl.Do(r.Context(), "GET", "/groups?min_access_level=30&per_page=50", nil, &groups); err != nil {
		WriteError(w, 502, "%s", strs.Truncate(err.Error(), 200))
		return
	}
	out := []RemoteNamespace{}
	for _, g := range groups {
		out = append(out, RemoteNamespace{g.ID, g.FullPath})
	}
	WriteJSON(w, 200, RemoteNamespaces{Namespaces: out, Configured: true})
}

// RemoteNamespaces are the provider groups a project's remote may go under.
type RemoteNamespaces struct {
	Namespaces []RemoteNamespace `json:"namespaces"`
	// Configured: this AppTown has a GitLab connection to create copies with.
	Configured bool `json:"configured"`
}

// RemoteNamespace is one provider group.
type RemoteNamespace struct {
	ID   int64  `json:"id"` // GitLab's
	Path string `json:"path"`
}

// RemoteAttached is where a project's repository was pushed.
type RemoteAttached struct {
	AttachmentID ids.ID `json:"attachmentId"`
	Remote       string `json:"remote"`
	Pushed       string `json:"pushed"`
}

// AttachRemote: POST /projects/{slug}/repo/remote
// {url} attaches an existing GitLab repo; {create: true} creates one via the
// API. Either way the default branch is pushed.
func (h SCMRemotes) AttachRemote(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in RemoteRequest) {
		identity, err := h.attachments.Attach(r.Context(), scm.AttachmentRequest{ProjectID: p.ID, RequestID: in.RequestID, ExpectedAttachmentID: in.ExpectedAttachmentID, URL: in.URL, Create: in.Create, NamespaceID: in.NamespaceID, RepositoryName: in.RepositoryName, RecoverProviderID: in.RecoverProviderID})
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, RemoteAttached{Remote: identity.CloneURL, Pushed: p.DefaultBranch, AttachmentID: identity.AttachmentID})
	})
}

type repositoryProvider interface {
	Client(ctx context.Context) (*gitlab.Client, error)
}

// RemoteRequest is POST /projects/{slug}/repo/remote: a remote to attach, or
// one to create in a namespace.
type RemoteRequest struct {
	RequestID            ids.ID `json:"requestId"`
	ExpectedAttachmentID ids.ID `json:"expectedAttachmentId"`
	RepositoryName       string `json:"repositoryName"`
	RecoverProviderID    int64  `json:"recoverProviderId"`
	URL                  string `json:"url"`
	Create               bool   `json:"create"`
	NamespaceID          int    `json:"namespaceId"` // group id; falls back to GITLAB_NAMESPACE_ID secret
}

func (h SCMRemotes) RemoteState(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	state, err := h.attachments.State(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, state)
}
