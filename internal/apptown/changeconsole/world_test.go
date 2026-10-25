package changeconsole

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/preview"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/apptown/workcommands"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/consoletest"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
	"github.com/flokkit/apptown/stock"
)

var databaseSuite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M) { os.Exit(databaseSuite.Run(m)) }

// world is a change console with a worker for chats: a project's
// repository and one change in progress, set up through their owners; the
// chat service with chats placed on a real worker server over mutual TLS,
// whose fake chat runtime has a real work tree a test can commit in.
type world struct {
	db       *database.DB
	dataDir  string
	hub      string
	folder   string
	project  project.Project
	change   *workflow.Change
	work     *workflow.Service
	owner    context.Context
	sessions *console.Service
	remote   *worker.RemoteAgent
	onWork   *consoletest.Agent // the runtime on the worker
	srv      interface{ Close() }
	workers  worker.SessionClient
	// narratives records the narrative refreshes asked for.
	narratives *askedNarratives
	// failSaves makes the worker's answers to saves fail on the way back.
	failSaves *atomic.Bool
	saves     *Saves
	chats     *Service
}

// failing fails requests for saves while fail is set: a worker that
// answers everything but its chat's commits.
type failing struct {
	base http.RoundTripper
	fail *atomic.Bool
}

func (f failing) RoundTrip(r *http.Request) (*http.Response, error) {
	if f.fail.Load() && strings.HasSuffix(r.URL.Path, "/save") {
		return nil, errors.New("the connection dropped")
	}
	return f.base.RoundTrip(r)
}

// noMirror publishes to no mirror: the project has no origin.
type noMirror struct{}

func (noMirror) PushBranch(context.Context, ids.ID, string, string) {}

// askedNarratives refreshes no narrative; it records the Changes a refresh
// was asked for.
type askedNarratives struct {
	mu      sync.Mutex
	changes []ids.ID
}

func (n *askedNarratives) Request(_ context.Context, changeID ids.ID) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.changes = append(n.changes, changeID)
	return nil
}

func (n *askedNarratives) asked() []ids.ID {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]ids.ID(nil), n.changes...)
}

func newWorld(t *testing.T) world { t.Helper(); return newWorldWith(t, nil) }

// newWorldWith is newWorld with previews on a worker when previews is not
// nil: a real Deployment over the world's records, booting changes'
// previews through previews and reached at a preview domain, so a saved
// commit is rehearsed.
func newWorldWith(t *testing.T, previews preview.Worker) world {
	t.Helper()
	db := databaseSuite.New(t)
	dataDir := t.TempDir()
	accounts := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := accounts.SeedAdministrator(context.Background(), "owner"); err != nil {
		t.Fatal(err)
	}
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "game", Name: "Game"})
	})
	if err != nil {
		t.Fatal(err)
	}
	projects := project.New(db, identity.Scoped(accounts, "project"))
	hub := filepath.Join(dataDir, "workspaces", "project-"+id.String())
	if err := scm.InitializeRepository(t.Context(), hub, "main"); err != nil {
		t.Fatal(err)
	}
	if err := projects.SetDefaultBranch(context.Background(), id, "main"); err != nil {
		t.Fatal(err)
	}
	p, err := project.ByIDIn(context.Background(), db, id)
	if err != nil {
		t.Fatal(err)
	}
	locks := &scm.RepositoryLocks{}
	work := workflow.New(context.Background(), db, workflow.Dependencies{
		Authority:    access.Checked{Authority: identity.Scoped(accounts, "workflow"), Projects: projects},
		Repositories: locks,
		Git:          workflow.GitWorkspace{DataDir: dataDir},
	})
	t.Cleanup(work.Stop)
	owner := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{Kind: "user", Name: "owner", Via: "session"}, Receipt: "owner", Verified: true})
	change, err := work.Create(owner, workflow.ChangeDraft{ProjectID: id, Title: "Work on the worker"})
	if err != nil {
		t.Fatal(err)
	}

	ca := workertest.NewCA(t)
	onWork := consoletest.NewAgent()
	srv := workertest.StartServer(t, ca, &worker.Server{State: t.TempDir(), Sessions: onWork})
	client := worker.SessionClient{URL: srv.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	failSaves := &atomic.Bool{}
	client.Client.Transport = failing{base: client.Client.Transport, fail: failSaves}
	remote := &worker.RemoteAgent{Sessions: client, Image: "apptown-agent:test", CopyEvery: 50 * time.Millisecond, Root: filepath.Join(dataDir, "console")}
	var sessions *console.Service
	placed := console.Placed{Local: consoletest.NewAgent(), Remote: remote, OnRemote: func(id string) (bool, error) {
		meta, err := sessions.Recorded(id)
		if errors.Is(err, console.ErrNoSession) {
			return false, nil
		}
		return err == nil && conversation.OnWorkerRecord(meta), err
	}}
	sessions = console.New(console.Dependencies{Agent: placed, Config: console.Config{DataDir: dataDir}})
	t.Cleanup(func() { _ = sessions.Shutdown(context.Background()) })

	branches := scm.NewBranches(db, locks)
	saves := NewSaves(SavesDependencies{DB: db, Sessions: sessions, Remote: remote, Branches: branches, Publisher: noMirror{}, DataDir: dataDir})
	// Without a worker for previews, Previews is nil: the apptown runs no
	// previews on a worker, so a saved commit is never rehearsed
	// (preview.Service.PreviewsOnWorker).
	var previewer *preview.Service
	if previews != nil {
		previewer = previewOwner(t, db, dataDir, projects, previews)
	}
	narratives := &askedNarratives{}
	chats := New(saves, Dependencies{DB: db, Sessions: sessions, Chats: conversation.Chats{Sessions: sessions, DB: db}, Previews: previewer,
		Narratives: narratives, Folders: workcommands.NewPreparation(db, nil, branches, dataDir, nil, saves, nil, nil),
		DataDir: dataDir, Stop: make(chan struct{}), Engine: context.Background()})
	t.Cleanup(remote.Close) // first: no copy or save outlives the world
	return world{db: db, dataDir: dataDir, hub: hub, folder: filepath.Join(dataDir, "workspaces", fmt.Sprintf("change-%d", change.ID)),
		project: p, change: change, work: work, owner: owner, sessions: sessions, remote: remote, onWork: onWork, srv: srv,
		workers: client, failSaves: failSaves, saves: saves, chats: chats, narratives: narratives}
}

// previewOwner is Preview with changes' previews on a worker: the
// preview's values are prepared here from the project's records and stock
// dependency catalog, and nothing runs on this machine's Docker.
func previewOwner(t *testing.T, db *database.DB, dataDir string, projects *project.Projects, previews preview.Worker) *preview.Service {
	t.Helper()
	secrets := secret.NewService(db, secret.Config{DataDir: dataDir})
	catalog := dependency.NewCatalog(db, nil)
	if _, _, err := catalog.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	targets := deployment.NewTargets(db, nil)
	integrations := integration.NewService(db, integration.Dependencies{Kinds: catalog, Projects: projects, Secrets: secrets})
	return preview.New(db, preview.Dependencies{
		Worker: previews, Targets: targets, Deployments: deployment.NewDeployments(db), Projects: projects, Integrations: integrations, DataDir: dataDir,
		Hosts: deployment.NewHosts(targets, secrets, deployment.HostSettings{Scope: docker.Scope{Client: dockerfake.New()}, DataDir: dataDir}),
		Environment: deployment.NewEnvironment(targets, deployment.EnvironmentDependencies{ToolVersions: stock.Versions(), Secrets: secrets, Needs: catalog, Integrations: integrations,
			Routes: deployment.Routing{PreviewScheme: "https", PreviewDomain: "preview.example.com"}, Projects: projects}),
	})
}

// openOnWorker opens the change's chat on the worker as the chat's opening
// does (conversation.Admission): the chat takes the change, its folder is
// prepared and its committed work published, and its record places it on
// the worker.
func (w world) openOnWorker(t *testing.T) *console.Session {
	t.Helper()
	id := w.sessions.NewSessionID()
	if err := w.db.InTx(w.owner, func(tx *database.Tx) error {
		_, err := workflow.TakeChangeIn(w.owner, tx, w.project.ID, w.change.Branch, scm.ChatHolder(id), nil, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.work.PrepareWorkspace(w.owner, w.change.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.chats.Placement().Publish(w.owner, &w.project, w.change, scm.ChatHolder(id)); err != nil {
		t.Fatal(err)
	}
	labels := conversation.LabelsFor(&w.project, w.change)
	labels[conversation.PlacementLabel] = conversation.OnWorker
	s, err := w.sessions.Open(context.Background(), console.OpenRequest{ID: id, Scope: conversation.Key(w.project.ID, w.change.ID), Labels: labels,
		Actor: "owner", Model: "fake/model", Workspace: console.Workspace{Path: w.folder, Writable: true}, Docker: true})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// warmOnWorker opens the chat on the worker and starts it there.
func (w world) warmOnWorker(t *testing.T) *console.Session {
	t.Helper()
	s := w.openOnWorker(t)
	if err := w.sessions.Warm(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	return s
}

// commitOnWorker commits a file in the chat's work tree on the worker.
func (w world) commitOnWorker(t *testing.T, id, name, body string) string {
	t.Helper()
	w.writeOnWorker(t, id, name, body)
	work := w.onWork.Started(id).Workspace.Path
	gittest.Run(t, work, "add", "-f", name)
	gittest.Run(t, work, "-c", "user.name=chat", "-c", "user.email=chat@example.org", "commit", "-qm", "chat: "+name)
	return gittest.Run(t, work, "rev-parse", "HEAD")
}

func (w world) writeOnWorker(t *testing.T, id, name, body string) {
	t.Helper()
	work := w.onWork.Started(id).Workspace.Path
	if work == "" {
		t.Fatalf("chat %s has not started on the worker", id)
	}
	path := filepath.Join(work, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// branchTip is the change's branch in the project's repository.
func (w world) branchTip() string {
	return scm.Ref(w.owner, w.hub, "refs/heads/"+w.change.Branch)
}

// holder is who holds the change, as its branch's authority says.
func (w world) holder(t *testing.T) string {
	t.Helper()
	authority, err := scm.ReadAuthorityIn(context.Background(), w.db, w.change.ProjectID, w.change.Branch)
	if err != nil {
		t.Fatal(err)
	}
	return authority.Holder
}

// workerChat is the change's one chat on the worker as the owner lists it.
func (w world) workerChat(t *testing.T) WorkerChat {
	t.Helper()
	chats, err := w.chats.WorkerChats(context.Background(), w.change)
	if err != nil || len(chats) != 1 {
		t.Fatalf("the change's worker chats: %+v %v", chats, err)
	}
	return chats[0]
}

// eventually polls cond until it holds, failing after ten seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	if !poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, cond) {
		t.Fatalf("never: %s", what)
	}
}
