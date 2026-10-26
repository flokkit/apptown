package projectlifecycle

import (
	"context"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/gitlab"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// noSessions is a console with no sessions for the deleted project.
type noSessions struct{}

func (noSessions) List(context.Context, console.Match) []*console.Session { return nil }
func (noSessions) Remove(context.Context, string) error                   { return nil }
func (noSessions) RemoveEnded(console.Match) error                        { return nil }

// cleanupPreviews records the frozen inventory handed to its owner.
type cleanupPreviews struct {
	calls   []string
	failure error
}

func (p *cleanupPreviews) RemoveReleaseForGood(_ context.Context, id ids.ID, label string, number int64) error {
	p.calls = append(p.calls, fmt.Sprintf("release:%s:%s:%d", id, label, number))
	return p.failure
}
func (p *cleanupPreviews) RemoveRunForGood(_ context.Context, id ids.ID) error {
	p.calls = append(p.calls, fmt.Sprintf("run:%s", id))
	return p.failure
}
func (p *cleanupPreviews) RemoveRunStepsForGood(_ context.Context, ids []ids.ID) error {
	for _, id := range ids {
		p.calls = append(p.calls, fmt.Sprintf("step:%s", id))
	}
	return p.failure
}
func (p *cleanupPreviews) RemoveChangeForGood(_ context.Context, id ids.ID) error {
	p.calls = append(p.calls, fmt.Sprintf("change:%s", id))
	return p.failure
}

// gitlabAt opens a GitLab client for a test server.
type gitlabAt string

func (g gitlabAt) Client(ctx context.Context) (*gitlab.Client, error) {
	return gitlab.New(string(g), "test-token")
}

// cleanupFixture is a cleanup over a fresh database and the given GitLab,
// with the deletion of project 100 recorded under plan.
func cleanupFixture(t *testing.T, gl GitLabClient, plan CleanupPlan) Cleanup {
	t.Helper()
	db := databaseSuite.New(t)
	if plan.ReleasePreviews == nil {
		plan.ReleasePreviews = &ReleasePreviewInventory{Version: 1, ProjectID: idstest.N(100), ServingLabel: plan.Slug, Numbers: []int64{}}
	}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return project.RecordDeletionIn(context.Background(), tx, idstest.N(100), plan.Slug, access.Actor{Name: "boss"}, plan, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	return Cleanup{Projects: project.New(db, nil), Sessions: noSessions{}, Docker: &docker.Scope{Client: docker.Refusing{}}, Previews: &cleanupPreviews{}, GitLab: gl, DB: db, DataDir: t.TempDir()}
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
	c := cleanupFixture(t, gitlabAt(server.URL), CleanupPlan{Slug: "game", GitLabPath: "team/game", GitLabArchive: "game-deleted-20260928-1234"})
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
	plan := CleanupPlan{RepositoryIdentity: fixtureCleanupIdentity(server.URL, idstest.N(100), "team/game"), Slug: "game", GitLabPath: "team/game", GitLabProjectID: 100, GitLabArchive: "game-deleted-100"}
	c := cleanupFixture(t, gitlabAt(server.URL), plan)
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

// A deleted project's preview that Preview could not wholly remove (on a
// worker: its stack and its build images) leaves the cleanup unfinished.
func TestProjectCleanupWhosePreviewWasNotWhollyRemovedIsNotDone(t *testing.T) {
	t.Parallel()
	c := cleanupFixture(t, gitlabAt(""), CleanupPlan{Slug: "previewed", ChangeIDs: []ids.ID{idstest.N(100)}})
	c.Previews = &cleanupPreviews{failure: errors.New("the worker did not answer")}
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
	c := cleanupFixture(t, gitlabAt(""), CleanupPlan{Slug: "owned"})
	for index, slug := range []string{"owned", "neighbor"} {
		dir := hubDir(c.DataDir, idstest.N(uint64(100+index)))
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
	if _, err := os.Stat(hubDir(c.DataDir, idstest.N(100))); !os.IsNotExist(err) {
		t.Fatalf("owned repository remains: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(hubDir(c.DataDir, idstest.N(101)), "keep")); err != nil || string(raw) != "neighbor" {
		t.Fatalf("wrong repository touched: %s %v", raw, err)
	}
}

func TestProjectCacheRetirementFailureHoldsCleanupAndRetriesItsRetainedID(t *testing.T) {
	t.Parallel()
	const owned = "apptown-project-cache-00000000-0064-7000-8000-000000000000"
	var removed []string
	fail := true
	client := dockerClientFunc(func(_ context.Context, _ string, cmd docker.Command) ([]byte, error) {
		if len(cmd.Args) == 3 && cmd.Args[0] == "volume" && cmd.Args[1] == "rm" {
			removed = append(removed, cmd.Args[2])
			if fail {
				return []byte("volume is in use"), errors.New("unavailable")
			}
			return []byte("Error response from daemon: No such volume: " + cmd.Args[2]), errors.New("absent")
		}
		t.Errorf("unexpected daemon command %v", cmd.Args)
		return nil, nil
	})
	c := cleanupFixture(t, nil, CleanupPlan{Slug: "retained"})
	c.Docker = &docker.Scope{Client: client}
	// The retained record is authoritative even if the plan's display slug differs.
	if failed := c.Clean(t.Context(), project.RepositoryRef{ID: idstest.N(100), Slug: "retained"}, CleanupPlan{Slug: "another-project", ReleasePreviews: &ReleasePreviewInventory{Version: 1, ProjectID: idstest.N(100), ServingLabel: "retained"}}); len(failed) == 0 {
		t.Fatal("cache removal failure was swallowed")
	}
	if _, err := c.Run(t.Context(), CleanupKey(idstest.N(100), 0), nil); err == nil {
		t.Fatal("failed cache retirement marked cleanup done")
	}
	d, err := c.Projects.Deletion(t.Context(), idstest.N(100))
	if err != nil || d.State != project.CleanupPending || !strings.Contains(d.Detail, "volume is in use") {
		t.Fatalf("failed cleanup: %+v %v", d, err)
	}
	_, err = database.Transact(t.Context(), c.DB, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "retained"})
	})
	if !errors.Is(err, project.ErrSlugTaken) {
		t.Fatalf("failed cleanup released slug: %v", err)
	}
	fail = false
	if _, err := c.Run(t.Context(), CleanupKey(idstest.N(100), 0), nil); err != nil {
		t.Fatal(err)
	}
	d, err = c.Projects.Deletion(t.Context(), idstest.N(100))
	if err != nil || d.State != project.CleanupDone {
		t.Fatalf("retry not done: %+v %v", d, err)
	}
	if len(removed) != 3 {
		t.Fatalf("retirement calls: %v", removed)
	}
	for _, v := range removed {
		if v != owned {
			t.Fatalf("removed foreign volume %s", v)
		}
	}
}

func fixtureCleanupIdentity(base string, projectID ids.ID, path string) *scm.RepositoryIdentity {
	return &scm.RepositoryIdentity{AttachmentID: idstest.N(7901), ProjectID: projectID, Scope: scm.ProviderScope{ConnectionID: idstest.N(7902), Kind: "gitlab", Base: base, AccountID: 41}, ProviderProjectID: 100, CloneURL: base + "/" + path + ".git", Path: path}
}
func (g gitlabAt) VerifyRecorded(ctx context.Context, identity scm.RepositoryIdentity) (scm.VerifiedRepository, error) {
	if identity.Scope.Base != string(g) {
		return scm.VerifiedRepository{}, fmt.Errorf("provider scope changed")
	}
	client, err := g.Client(ctx)
	if err != nil {
		return scm.VerifiedRepository{}, err
	}
	observed, err := client.Project(ctx, strconv.FormatInt(identity.ProviderProjectID, 10))
	if err != nil {
		return scm.VerifiedRepository{}, err
	}
	if observed.ID != identity.ProviderProjectID {
		return scm.VerifiedRepository{}, fmt.Errorf("provider object changed")
	}
	return scm.VerifiedRepository{Identity: identity, Provider: scm.ProviderSession{Scope: identity.Scope, Client: client}, Observed: observed}, nil
}

func TestCleanupRetriesTheSameFrozenPreviewInventoryWithoutSourceRows(t *testing.T) {
	t.Parallel()
	run, step, change := idstest.N(21), idstest.N(22), idstest.N(23)
	plan := CleanupPlan{Slug: "frozen", RunIDs: []ids.ID{run}, PreviewSteps: []ids.ID{step}, ChangeIDs: []ids.ID{change}}
	c := cleanupFixture(t, gitlabAt(""), plan)
	owner := &cleanupPreviews{failure: errors.New("preview unavailable")}
	c.Previews = owner
	expected := []string{"run:" + run.String(), "step:" + step.String(), "change:" + change.String()}
	if _, err := c.Run(t.Context(), CleanupKey(idstest.N(100), 0), nil); err == nil {
		t.Fatal("owner failure marked cleanup successful")
	}
	d, err := c.Projects.Deletion(t.Context(), idstest.N(100))
	if err != nil || d.State != project.CleanupPending || d.Attempts != 1 || !strings.Contains(d.Detail, "preview unavailable") {
		t.Fatalf("failed cleanup evidence: %+v %v", d, err)
	}
	if !slices.Equal(owner.calls, expected) {
		t.Fatalf("frozen inventory/order: %v", owner.calls)
	}
	owner.failure = nil
	owner.calls = nil
	if _, err := c.Run(t.Context(), CleanupKey(idstest.N(100), 0), nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(owner.calls, expected) {
		t.Fatalf("retry changed frozen inventory: %v", owner.calls)
	}
	d, err = c.Projects.Deletion(t.Context(), idstest.N(100))
	if err != nil || d.State != project.CleanupDone || d.Attempts != 2 {
		t.Fatalf("retry outcome: %+v %v", d, err)
	}
}

type dockerClientFunc func(context.Context, string, docker.Command) ([]byte, error)

func (f dockerClientFunc) Run(ctx context.Context, host string, req docker.Command) ([]byte, error) {
	return f(ctx, host, req)
}

func TestHistoricalPreviewCleanupRequiresVerifiedRetainedInventory(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		inventory *ReleasePreviewInventory
	}{
		{"legacy-missing", nil},
		{"foreign-owner", &ReleasePreviewInventory{Version: 1, ProjectID: idstest.N(101), ServingLabel: "other"}},
		{"unsupported-version", &ReleasePreviewInventory{Version: 2, ProjectID: idstest.N(100), ServingLabel: "owned"}},
		{"unsafe-label", &ReleasePreviewInventory{Version: 1, ProjectID: idstest.N(100), ServingLabel: "../other"}},
		{"invalid-number", &ReleasePreviewInventory{Version: 1, ProjectID: idstest.N(100), ServingLabel: "owned", Numbers: []int64{0}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := cleanupFixture(t, nil, CleanupPlan{Slug: "owned"})
			previews := &cleanupPreviews{}
			c.Previews = previews
			dir := hubDir(c.DataDir, idstest.N(100))
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			failures := c.Clean(t.Context(), project.RepositoryRef{ID: idstest.N(100), Slug: "owned"}, CleanupPlan{Slug: "owned", ReleasePreviews: tc.inventory})
			if len(failures) == 0 || len(previews.calls) != 0 {
				t.Fatalf("unknown ownership had effects/success: %v %v", failures, previews.calls)
			}
			if _, err := os.Stat(dir); err != nil {
				t.Fatalf("unknown inventory removed repository: %v", err)
			}
		})
	}
	c := cleanupFixture(t, nil, CleanupPlan{Slug: "empty"})
	if _, err := c.Run(t.Context(), CleanupKey(idstest.N(100), 0), nil); err != nil {
		t.Fatal(err)
	}
	d, err := c.Projects.Deletion(t.Context(), idstest.N(100))
	if err != nil || d.State != project.CleanupDone {
		t.Fatalf("verified empty: %+v %v", d, err)
	}
}

func TestHistoricalPreviewRetirementRetriesRetainedIdentityAfterRowsAreGone(t *testing.T) {
	t.Parallel()
	plan := CleanupPlan{Slug: "renamed", ReleasePreviews: &ReleasePreviewInventory{Version: 1, ProjectID: idstest.N(100), ServingLabel: "original", Numbers: []int64{1, 9, 105}}}
	c := cleanupFixture(t, nil, plan)
	owner := &cleanupPreviews{failure: errors.New("release unavailable")}
	c.Previews = owner
	for _, fail := range []bool{true, false} {
		owner.calls = nil
		if !fail {
			owner.failure = nil
		}
		_, err := c.Run(t.Context(), CleanupKey(idstest.N(100), 0), nil)
		if (err != nil) != fail {
			t.Fatalf("retirement failure=%v: %v", fail, err)
		}
		want := []string{"release:00000000-0064-7000-8000-000000000000:original:1", "release:00000000-0064-7000-8000-000000000000:original:9", "release:00000000-0064-7000-8000-000000000000:original:105"}
		if !slices.Equal(owner.calls, want) {
			t.Fatalf("changed retained inventory: %v", owner.calls)
		}
		d, err := c.Projects.Deletion(t.Context(), idstest.N(100))
		wantState := project.CleanupDone
		if fail {
			wantState = project.CleanupPending
		}
		if err != nil || d.State != wantState {
			t.Fatalf("retirement receipt: %+v %v", d, err)
		}
	}
}
