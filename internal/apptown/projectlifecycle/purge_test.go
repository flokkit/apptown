package projectlifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
)

// fakeGitLab is a GitLab with the project's copy as project 100 at
// team/p: it records renames and archives, and refuses to archive while
// broken.
type fakeGitLab struct {
	// archiving runs as the copy is archived.
	archiving func()
	mu        sync.Mutex
	path      string
	archived  bool
	broken    bool
	mutations int
}

func (g *fakeGitLab) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/v4/projects/100":
		fmt.Fprintf(w, `{"id":100,"archived":%t,"path_with_namespace":%q}`, g.archived, g.path)
	case g.broken && r.Method == "POST":
		http.Error(w, "GitLab is down", http.StatusBadGateway)
	case r.Method == "PUT" && r.URL.Path == "/api/v4/projects/100":
		var body struct{ Path string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		g.mutations++
		g.path = "team/" + body.Path
		fmt.Fprint(w, `{}`)
	case r.Method == "POST" && r.URL.Path == "/api/v4/projects/100/archive":
		if g.archiving != nil {
			g.archiving()
		}
		g.mutations++
		g.archived = true
		fmt.Fprint(w, `{}`)
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusBadRequest)
	}
}

// gitlabIDs names the copy at team/p as GitLab project 100.
type gitlabIDs struct{}

func (gitlabIDs) ProjectID(ctx context.Context, path string) (int64, error) {
	if path != "team/p" {
		return 0, fmt.Errorf("no GitLab project at %s", path)
	}
	return 100, nil
}

// leftoverSets leaves the named set behind while left is set.
type leftoverSets struct {
	*scriptedDeployments
	mu   sync.Mutex
	left []string
	// after runs once the sets are removed.
	after func()
}

func (l *leftoverSets) PurgeProjectBackups(ctx context.Context, _ ids.ID, allowed func(context.Context) error) (int, []string, error) {
	if err := allowed(ctx); err != nil {
		return 0, nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.after != nil {
		l.after()
	}
	return 2, l.left, nil
}

type purgeWorld struct {
	*deletionWorld
	gitlab *fakeGitLab
	ops    *operations.Service
	sets   *leftoverSets
	hub    string
}

// newPurgeWorld is a deleted project kept until its purge, whose hub
// repository's origin is its GitLab copy at team/p.
func newPurgeWorld(t *testing.T) *purgeWorld {
	t.Helper()
	w := &purgeWorld{deletionWorld: newDeletionWorld(t, "main"), gitlab: &fakeGitLab{path: "team/p"}}
	server := httptest.NewServer(http.HandlerFunc(w.gitlab.serve))
	t.Cleanup(server.Close)
	if err := jobs.Migrate(context.Background(), w.db.Pool()); err != nil {
		t.Fatal(err)
	}
	ids := identity.New(w.db, identity.Config{Projects: project.NewCatalogue(w.db)})
	w.ops = operations.New(w.db, allowAll{}, ids, nil)
	w.sets = &leftoverSets{scriptedDeployments: w.deps}
	w.hub = hubDir(w.d.lifecycle.dataDir, "p")
	if err := os.MkdirAll(w.hub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"remote", "add", "origin", "https://gitlab.example/team/p.git"}} {
		cmd := gittest.Command(args...)
		cmd.Dir = w.hub
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	w.d = NewDeletion(w.d.lifecycle, DeletionDependencies{Sessions: w.d.deps.Sessions, Records: w.deps, Data: w.sets, Queue: noQueue{},
		Authority: allowAll{}, Operations: w.ops, GitLab: gitlabIDs{}, Archive: Cleanup{GitLab: gitlabAt(server.URL)}, Now: func() time.Time { return w.clock }})
	if _, err := w.d.Request(context.Background(), w.project); err != nil {
		t.Fatal(err)
	}
	w.clock = w.clock.Add(DeletionGrace + time.Minute)
	w.pass()
	if l := w.lifecycle(); l.State != project.Deleted {
		t.Fatalf("the world's project: %+v", l)
	}
	return w
}

// purgeDue moves the clock past the kept period and runs the purge pass
// and the purge it admits.
func (w *purgeWorld) purgeDue() *operations.Operation {
	w.t.Helper()
	w.clock = w.clock.Add(DefaultKeptDays*24*time.Hour + time.Hour)
	if _, err := w.d.PurgePass(context.Background(), "", nil); err != nil {
		w.t.Fatal(err)
	}
	st, err := w.d.Status(context.Background(), w.project)
	if err != nil || st.Purge == nil {
		w.t.Fatalf("no purge admitted: %+v %v", st, err)
	}
	op, err := w.ops.RunHere(context.Background(), st.Purge.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return op
}

func (w *purgeWorld) exists() bool {
	w.t.Helper()
	_, err := project.ByIDIn(context.Background(), w.db, w.project)
	found, err := database.Lookup(err)
	if err != nil {
		w.t.Fatal(err)
	}
	return found
}

// The scheduled purge runs under the deletion's binding, not as the person
// who deleted: it removes the backup sets and the hub repository, renames
// and archives the GitLab copy (never deletes it), then removes the records
// with a receipt naming the binding and the deleter.
//
// Kills: a purge that deletes the GitLab copy or skips it; records removed
// before the copy is archived; a purge run as the deleter.
func TestTheScheduledPurgeArchivesTheGitLabCopyThenRemovesTheRecords(t *testing.T) {
	t.Parallel()
	w := newPurgeWorld(t)
	st, err := w.d.Status(context.Background(), w.project)
	if err != nil {
		t.Fatal(err)
	}
	op := w.purgeDue()
	if op.State != operations.Succeeded {
		t.Fatalf("purge: %s %q", op.State, op.Detail)
	}
	if op.Authority.Credential != identity.BindingCredential(st.BindingID) || op.Authority.Person != "boss" {
		t.Fatalf("the purge ran as %+v, not the deletion's binding", op.Authority)
	}
	if w.gitlab.path != fmt.Sprintf("team/p-deleted-%d", w.project) || !w.gitlab.archived {
		t.Fatalf("the GitLab copy: %s archived=%v", w.gitlab.path, w.gitlab.archived)
	}
	if _, err := os.Stat(w.hub); !os.IsNotExist(err) {
		t.Fatalf("the hub repository stays: %v", err)
	}
	if w.exists() {
		t.Fatal("the purged project's records stand")
	}
	// The slug stays held by the cleanup the purge queued.
	if d, err := w.d.lifecycle.projects.Deletion(context.Background(), w.project); err != nil || d.Slug != "p" || d.State != project.CleanupPending {
		t.Fatalf("the purge's cleanup: %+v %v", d, err)
	}
	receipts, err := audit.Recent(context.Background(), w.db, audit.Filter{ProjectID: w.project, KindPrefix: "project.purged"})
	if err != nil || len(receipts) != 1 || !strings.Contains(string(receipts[0].Payload), fmt.Sprintf(`"binding":%q`, st.BindingID)) || !strings.Contains(string(receipts[0].Payload), `"deleter":"boss"`) {
		t.Fatalf("purge receipts: %+v %v", receipts, err)
	}
}

// A purge that cannot archive the GitLab copy, or leaves a backup set,
// ends failed with what is left named, keeps the records (so the slug
// stays taken) and closes Restore; the next day's purge finishes, taking
// an already-archived copy as archived.
//
// Kills: records removed after a failed step; a failed purge read as done;
// a repeat that renames or archives the copy again.
func TestAFailedPurgeKeepsTheRecordsAndTheNextOneFinishes(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"gitlab", "backup set"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			w := newPurgeWorld(t)
			if fault == "gitlab" {
				w.gitlab.broken = true
			} else {
				w.sets.left = []string{"set 3 of main: the store refused"}
			}
			op := w.purgeDue()
			want := map[string]string{"gitlab": "the GitLab copy at team/p", "backup set": "set 3 of main"}[fault]
			if op.State != operations.Failed || !strings.Contains(op.Detail, "purge incomplete") || !strings.Contains(op.Detail, want) {
				t.Fatalf("a purge that could not finish: %s %q", op.State, op.Detail)
			}
			if !w.exists() || w.lifecycle().State != project.Deleted {
				t.Fatalf("a failed purge removed the records: %+v", w.lifecycle())
			}
			if _, err := w.d.Restore(context.Background(), w.project); err == nil || !strings.Contains(err.Error(), "purge has started") {
				t.Fatalf("restore after a failed purge: %v", err)
			}
			w.gitlab.broken, w.sets.left = false, nil
			w.gitlab.mu.Lock()
			before := w.gitlab.mutations
			w.gitlab.mu.Unlock()
			if again := w.purgeDue(); again.State != operations.Succeeded || again.ID == op.ID {
				t.Fatalf("the next purge: %d %s %q", again.ID, again.State, again.Detail)
			}
			if w.exists() {
				t.Fatal("the records stand after the purge finished")
			}
			// The copy renamed before the archive failed is not renamed
			// again: the repeat only archives it.
			if fault == "gitlab" && w.gitlab.mutations != before+1 {
				t.Fatalf("the repeat changed the copy %d times, want once", w.gitlab.mutations-before)
			}
		})
	}
}

// Revoking the deletion's binding stops a purge at its next step: nothing
// more is removed and the records stay.
//
// Kills: a purge that goes on under withdrawn authority.
func TestARevokedBindingStopsThePurge(t *testing.T) {
	t.Parallel()
	w := newPurgeWorld(t)
	w.clock = w.clock.Add(DefaultKeptDays*24*time.Hour + time.Hour)
	if _, err := w.d.PurgePass(context.Background(), "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.RevokeBinding(context.Background(), w.project, "keep it after all"); err != nil {
		t.Fatal(err)
	}
	st, err := w.d.Status(context.Background(), w.project)
	if err != nil || st.Purge == nil || !st.BindingRevoked {
		t.Fatalf("status: %+v %v", st, err)
	}
	op, err := w.ops.RunHere(context.Background(), st.Purge.ID)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != operations.Failed || !strings.Contains(op.Detail, "authority withdrawn") {
		t.Fatalf("a purge under a revoked binding: %s %q", op.State, op.Detail)
	}
	if !w.exists() || w.gitlab.mutations != 0 {
		t.Fatalf("a purge under a revoked binding removed something (gitlab mutations %d)", w.gitlab.mutations)
	}
	if _, err := os.Stat(w.hub); err != nil {
		t.Fatalf("the hub repository went: %v", err)
	}
}

// The binding is asked again before each step: revoked while the backup
// sets go, the hub repository stays; revoked while the GitLab copy is
// archived, the records stay.
//
// Kills: a step that does not ask before its effect.
func TestAPurgeAsksBeforeEachStep(t *testing.T) {
	t.Parallel()
	for _, during := range []string{"backup sets", "gitlab"} {
		t.Run(during, func(t *testing.T) {
			t.Parallel()
			w := newPurgeWorld(t)
			revoke := func() {
				l := w.lifecycle()
				if err := w.db.InTx(context.Background(), func(tx *database.Tx) error {
					return identity.RevokeBindingIn(context.Background(), tx, l.BindingID, "boss", "stop", time.Now())
				}); err != nil {
					t.Error(err)
				}
			}
			if during == "backup sets" {
				w.sets.after = revoke
			} else {
				w.gitlab.archiving = revoke
			}
			if op := w.purgeDue(); op.State != operations.Failed || !strings.Contains(op.Detail, "authority withdrawn") {
				t.Fatalf("a purge revoked during %s: %s %q", during, op.State, op.Detail)
			}
			if !w.exists() {
				t.Fatal("the records went under a revoked binding")
			}
			if _, err := os.Stat(w.hub); during == "backup sets" && err != nil {
				t.Fatalf("the hub repository went under a revoked binding: %v", err)
			}
		})
	}
}

// Purge now runs under the administrator's own authority, before the kept
// period ends.
func TestPurgeNowRunsAsTheAdministrator(t *testing.T) {
	t.Parallel()
	w := newPurgeWorld(t)
	if _, err := w.d.Restore(context.Background(), w.project); err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.PurgeNow(context.Background(), w.project); err == nil || !strings.Contains(err.Error(), "not deleted") {
		t.Fatalf("purge now of an active project: %v", err)
	}
	if _, err := w.d.Request(context.Background(), w.project); err != nil {
		t.Fatal(err)
	}
	w.clock = w.clock.Add(DeletionGrace + time.Minute)
	w.pass()
	op, err := w.d.PurgeNow(context.Background(), w.project)
	if err != nil {
		t.Fatal(err)
	}
	if op.Authority.Credential != "session:1" || op.Authority.Name != "boss" || op.Authority.Via != "session" {
		t.Fatalf("purge now was admitted as %+v", op.Authority)
	}
}

// Restore and the first purge admission decide under the project's row
// lock: racing, exactly one wins.
//
// Kills: a restored project whose purge was admitted; neither winning.
func TestRestoreAndThePurgeAdmissionRaceOneWins(t *testing.T) {
	t.Parallel()
	for range 5 {
		w := newPurgeWorld(t)
		var wg sync.WaitGroup
		var restoreErr, purgeErr error
		wg.Add(2)
		go func() { defer wg.Done(); _, restoreErr = w.d.Restore(context.Background(), w.project) }()
		go func() { defer wg.Done(); _, purgeErr = w.d.PurgeNow(context.Background(), w.project) }()
		wg.Wait()
		if (restoreErr == nil) == (purgeErr == nil) {
			t.Fatalf("restore %v, purge %v: exactly one wins", restoreErr, purgeErr)
		}
		l := w.lifecycle()
		if restoreErr == nil && l.State != project.Active || purgeErr == nil && !l.PurgeStartedAt.Valid {
			t.Fatalf("the winner's state: %+v", l)
		}
	}
}

// A copy of an eval holds synthetic data: its deletion has no grace and no
// kept period, and its purge is admitted as soon as it is deleted.
//
// Kills: an eval copy kept for 30 days.
func TestAnEvalCopyIsPurgedWithoutAKeptPeriod(t *testing.T) {
	t.Parallel()
	w := newDeletionWorld(t, "main")
	if err := jobs.Migrate(context.Background(), w.db.Pool()); err != nil {
		t.Fatal(err)
	}
	ids := identity.New(w.db, identity.Config{Projects: project.NewCatalogue(w.db)})
	ops := operations.New(w.db, allowAll{}, ids, nil)
	w.d = NewDeletion(w.d.lifecycle, DeletionDependencies{Sessions: w.d.deps.Sessions, Records: w.deps, Data: w.deps, Queue: noQueue{},
		Authority: allowAll{}, Operations: ops, Now: func() time.Time { return w.clock }})
	if _, err := w.d.RequestCopy(context.Background(), w.project, "alice"); err != nil {
		t.Fatal(err)
	}
	w.pass()
	st, err := w.d.Status(context.Background(), w.project)
	if err != nil || st.State != project.Deleted || st.Purge == nil || !st.KeptUntil.Time.Equal(st.DeletedAt.Time) {
		t.Fatalf("an eval copy after one pass: %+v %v", st, err)
	}
}
