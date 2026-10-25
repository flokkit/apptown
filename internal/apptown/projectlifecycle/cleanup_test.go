package projectlifecycle

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/gitlab"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// noSessions is a console with no sessions for the deleted project.
type noSessions struct{}

func (noSessions) List(context.Context, console.Match) []*console.Session { return nil }
func (noSessions) Remove(context.Context, string) error                   { return nil }
func (noSessions) RemoveEnded(console.Match) error                        { return nil }

// noWorker is an AppTown without a preview worker.
type noWorker struct{}

func (noWorker) RemovePreviewForGood(context.Context, string) error { return nil }

// gitlabAt opens a GitLab client for a test server.
type gitlabAt string

func (g gitlabAt) Client(ctx context.Context) (*gitlab.Client, error) {
	return gitlab.New(string(g), "test-token")
}

type dockerClientFunc func(context.Context, string, docker.Command) ([]byte, error)

func (f dockerClientFunc) Run(ctx context.Context, host string, req docker.Command) ([]byte, error) {
	return f(ctx, host, req)
}

// cleanupFixture is a cleanup over a fresh database and the given Docker,
// with the deletion of project 100 recorded under plan.
func cleanupFixture(t *testing.T, client docker.Client, gl GitLabClient, plan CleanupPlan) Cleanup {
	t.Helper()
	db := databaseSuite.New(t)
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return project.RecordDeletionIn(context.Background(), tx, idstest.N(100), plan.Slug, access.Actor{Name: "boss"}, plan, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	return Cleanup{Projects: project.New(db, nil), Sessions: noSessions{}, Docker: &docker.Scope{Client: client}, Previews: noWorker{}, GitLab: gl, DB: db, DataDir: t.TempDir()}
}

// A cleanup that never reached Docker is not done, and its name stays held.
func TestProjectCleanupThatNeverReachedDockerIsNotDone(t *testing.T) {
	t.Parallel()
	unreachable := dockerClientFunc(func(context.Context, string, docker.Command) ([]byte, error) {
		return []byte("error during connect: dial unix /var/run/docker.sock: connect: no such file or directory"), errors.New("exit status 1")
	})
	c := cleanupFixture(t, unreachable, gitlabAt(""), CleanupPlan{Slug: "preview-only", RunIDs: []ids.ID{idstest.N(100)}, ChangeIDs: []ids.ID{idstest.N(100)}})
	_, err := c.Run(context.Background(), CleanupKey(idstest.N(100), 0), nil)
	d, readErr := c.Projects.Deletion(context.Background(), idstest.N(100))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err == nil && d.State == project.CleanupDone {
		t.Fatal("cleanup released slug and marked done without reaching Docker")
	}
}

// Only the GitLab project the deletion named by id counts as archived: an
// unrelated project already at the archive path does not.
func TestProjectCleanupArchivesOnlyTheGitLabProjectItNamed(t *testing.T) {
	t.Parallel()
	mutations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v4/projects/team/game-deleted-20260928-1234" {
			fmt.Fprint(w, `{"id":99,"archived":true,"path_with_namespace":"team/game-deleted-20260928-1234"}`)
			return
		}
		if r.Method == "GET" && r.URL.Path == "/api/v4/projects/team/game" {
			fmt.Fprint(w, `{"id":100,"archived":false,"path_with_namespace":"team/game"}`)
			return
		}
		mutations++
		http.Error(w, "destination already exists", http.StatusConflict)
	}))
	defer server.Close()
	c := cleanupFixture(t, dockerfake.New(), gitlabAt(server.URL), CleanupPlan{Slug: "game", GitLabPath: "team/game", GitLabArchive: "game-deleted-20260928-1234"})
	_, err := c.Run(context.Background(), CleanupKey(idstest.N(100), 0), nil)
	d, readErr := c.Projects.Deletion(context.Background(), idstest.N(100))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err == nil && d.State == project.CleanupDone {
		t.Fatalf("cleanup marked done while original GitLab project 100 is untouched: mutations=%d", mutations)
	}
}

// The GitLab copy the deletion named is moved to its archive name and
// archived once; a later attempt finds it done and changes nothing.
func TestProjectCleanupMovesAndArchivesItsGitLabCopyOnce(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	path, archived, mutations := "team/game", false, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v4/projects/100":
			fmt.Fprintf(w, `{"id":100,"archived":%t,"path_with_namespace":%q}`, archived, path)
		case r.Method == "PUT" && r.URL.Path == "/api/v4/projects/100":
			mutations++
			path = "team/game-deleted-100"
			fmt.Fprint(w, `{}`)
		case r.Method == "POST" && r.URL.Path == "/api/v4/projects/100/archive":
			mutations++
			archived = true
			fmt.Fprint(w, `{}`)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, 400)
		}
	}))
	defer server.Close()
	plan := CleanupPlan{Slug: "game", GitLabPath: "team/game", GitLabProjectID: 100, GitLabArchive: "game-deleted-100"}
	c := cleanupFixture(t, dockerfake.New(), gitlabAt(server.URL), plan)
	if _, err := c.Run(context.Background(), CleanupKey(idstest.N(100), 0), nil); err != nil {
		t.Fatal(err)
	}
	if d, err := c.Projects.Deletion(context.Background(), idstest.N(100)); err != nil || d.State != project.CleanupDone {
		t.Fatalf("cleanup: %+v %v", d, err)
	}
	if path != "team/game-deleted-100" || !archived || mutations != 2 {
		t.Fatalf("gitlab: path=%s archived=%v mutations=%d", path, archived, mutations)
	}
	if err := c.archiveGitLab(t.Context(), plan); err != nil || mutations != 2 {
		t.Fatalf("a repeat attempt: %v mutations=%d", err, mutations)
	}
}

// failingPreviews is a preview worker that cannot confirm removing a
// change's preview (its build images among it).
type failingPreviews struct{}

func (failingPreviews) RemovePreviewForGood(_ context.Context, name string) error {
	if name == docker.PreviewChangeName(idstest.N(100)) {
		return errors.New("the worker did not answer")
	}
	return nil
}

// A deleted project's preview that Preview could not wholly remove (on a
// worker: its stack and its build images) leaves the cleanup unfinished.
func TestProjectCleanupWhosePreviewWasNotWhollyRemovedIsNotDone(t *testing.T) {
	t.Parallel()
	c := cleanupFixture(t, dockerfake.New(), gitlabAt(""), CleanupPlan{Slug: "previewed", ChangeIDs: []ids.ID{idstest.N(100)}})
	c.Previews = failingPreviews{}
	_, err := c.Run(context.Background(), CleanupKey(idstest.N(100), 0), nil)
	d, readErr := c.Projects.Deletion(context.Background(), idstest.N(100))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err == nil || d.State == project.CleanupDone {
		t.Fatalf("cleanup finished though a preview's removal was not confirmed: %v %+v", err, d)
	}
}

func TestCleanupUsesRetainedDeletionOwnershipAfterProjectRowsAreGone(t *testing.T) {
	t.Parallel()
	c := cleanupFixture(t, dockerfake.New(), gitlabAt(""), CleanupPlan{Slug: "owned"})
	for _, slug := range []string{"owned", "neighbor"} {
		dir := hubDir(c.DataDir, slug)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "keep"), []byte(slug), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// sql-guarantee: preserve deletion identity but forge a disagreeing cleanup payload; normal deletion cannot do this.
	if _, err := c.DB.Exec(`UPDATE project_deletions SET plan=jsonb_set(plan, '{slug}', '"neighbor"') WHERE project_id=?`, idstest.N(100)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := c.Run(t.Context(), CleanupKey(idstest.N(100), 0), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(hubDir(c.DataDir, "owned")); !os.IsNotExist(err) {
		t.Fatalf("owned repository remains: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(hubDir(c.DataDir, "neighbor"), "keep")); err != nil || string(raw) != "neighbor" {
		t.Fatalf("wrong repository touched: %s %v", raw, err)
	}
}
