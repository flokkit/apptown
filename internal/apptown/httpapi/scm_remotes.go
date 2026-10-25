package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/gitlab"
	"github.com/flokkit/apptown/internal/strs"
)

type SCMRemotes struct {
	projects *project.Projects
	provider RepositoryProvider
	secrets  *secret.Service
	db       *database.DB
	dataDir  string
	now      func() time.Time // AppTown's clock, which stamps its receipts; nil is the real one
}

func NewSCMRemotes(projects *project.Projects, provider RepositoryProvider, secrets *secret.Service, db *database.DB, dataDir string, now func() time.Time) SCMRemotes {
	return SCMRemotes{projects: projects, provider: provider, secrets: secrets, db: db, dataDir: dataDir, now: now}
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
	Remote string `json:"remote"`
	Pushed string `json:"pushed"`
}

// AttachRemote: POST /projects/{slug}/repo/remote
// {url} attaches an existing GitLab repo; {create: true} creates one via the
// API. Either way the default branch is pushed.
func (h SCMRemotes) AttachRemote(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	if p.DefaultBranch == "" {
		WriteError(w, 422, "project is not repo-backed; POST /projects/%s/repo first", p.Slug)
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in RemoteRequest) {
		repoDir, err := p.Repository(h.dataDir)
		if err != nil {
			WriteFault(w, err)
			return
		}
		remoteURL := in.URL
		if in.Create {
			gl, err := h.provider.Client(r.Context())
			if err != nil {
				WriteError(w, 422, "%v", err)
				return
			}
			ns := in.NamespaceID
			if ns == 0 {
				values, err := h.secrets.WithContext(r.Context())
				if err != nil {
					WriteFaultOr(w, err, http.StatusServiceUnavailable)
					return
				}
				fmt.Sscanf(values["GITLAB_NAMESPACE_ID"], "%d", &ns)
			}
			if ns == 0 {
				// No group named: the first group forge-bot can create in — the
				// wizard's own default — rather than the personal namespace,
				// which this GitLab refuses ("You cannot create projects in your
				// personal namespace").
				var groups []struct {
					ID int64 `json:"id"`
				}
				if gl.Do(r.Context(), "GET", "/groups?min_access_level=30&per_page=1", nil, &groups) == nil && len(groups) > 0 {
					ns = int(groups[0].ID)
				}
			}
			cloneURL, pathNS, err := gl.CreateProject(r.Context(), p.Slug, ns)
			if err != nil {
				WriteError(w, 502, "%v", err)
				return
			}
			remoteURL = cloneURL
			_ = pathNS
		}
		if remoteURL == "" {
			WriteError(w, 400, "url or create required")
			return
		}
		// Embed a username (not the token) so askpass is only asked for the password.
		if u, err := url.Parse(remoteURL); err == nil && u.User == nil {
			u.User = url.User("apptown")
			remoteURL = u.String()
		}
		_, _ = gitexec.Run(r.Context(), repoDir, "remote", "remove", "origin")
		if out, err := gitexec.Run(r.Context(), repoDir, "remote", "add", "origin", remoteURL); err != nil {
			WriteError(w, 500, "remote add: %s", out)
			return
		}
		env, err := h.provider.AskpassEnv(r.Context())
		if err != nil {
			WriteError(w, 422, "%v", err)
			return
		}
		if out, err := gitexec.RunEnv(context.WithoutCancel(r.Context()), repoDir, env, "push", "-u", "origin", p.DefaultBranch); err != nil {
			WriteError(w, 502, "initial push: %s", strs.Truncate(out, 300))
			return
		}
		audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, Actor(r), p.ID, "project.remote.attached", p.Slug, map[string]any{"remote": scm.PublicRemoteURL(remoteURL)})
		WriteJSON(w, 200, RemoteAttached{Remote: scm.PublicRemoteURL(remoteURL), Pushed: p.DefaultBranch})
	})
}

type RepositoryProvider interface {
	Client(ctx context.Context) (*gitlab.Client, error)
	AskpassEnv(ctx context.Context) ([]string, error)
}

// RemoteRequest is POST /projects/{slug}/repo/remote: a remote to attach, or
// one to create in a namespace.
type RemoteRequest struct {
	URL         string `json:"url"`
	Create      bool   `json:"create"`
	NamespaceID int    `json:"namespaceId"` // group id; falls back to GITLAB_NAMESPACE_ID secret
}
