package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/apptown/conversation"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

func purgeFixture(t *testing.T) (*App, *fakeDocker, []string) {
	return purgeFixtureState(t, false)
}

func purgeFixtureState(t *testing.T, live bool) (*App, *fakeDocker, []string) {
	t.Helper()
	f := newFakeDocker()
	a := workAuthorityFixture(t, Config{DockerClient: f})
	fx := fixtureWork(t, a)
	if !live {
		if _, err := a.Work.Abandon(asPerson("boss"), workflow.StopWork{RunID: fx.Run, Reason: "purge fixture is quiescent"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Work.AttachArtifact(asPerson("boss"), fx.Run, workflow.ArtifactDraft{Kind: "report", Name: "r", Body: "original evidence"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{1, 2} {
		if err := integration.NewAttachments(a.DB).Remember(t.Context(), idstest.N(uint64(id)), "errors", "test", `{}`); err != nil {
			t.Fatal(err)
		}
		if err := model.New(a.DB).Set(context.Background(), "purge", idstest.N(uint64(id)), map[int]string{1: "game", 2: "open"}[id], "default", "provider/x", "boss"); err != nil {
			t.Fatal(err)
		}
	}
	if err := model.New(a.DB).Set(context.Background(), "purge", ids.Nil, "", "default", "provider/x", "boss"); err != nil {
		t.Fatal(err)
	}
	if err := usage.New(a.DB).Record(context.Background(), usage.Entry{ProjectID: idstest.N(1), RunID: fx.Run, RootRunID: fx.Run, Kind: "agent", Actor: "owner"}, usage.Totals{Total: 123}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := dataprotection.NewBackups(a.DB).Record(context.Background(), &dataprotection.Backup{ProjectID: idstest.N(1), Deployment: "main", TakenAt: now(), Trigger: "test", Actor: "owner", Path: filepath.Join(a.cfg.dataDir, "backups", "retained-backup"), Status: dataprotection.BackupOK}, "owner"); err != nil {
		t.Fatal(err)
	}
	paths := []string{a.projectRepoPath("game"), filepath.Join(a.cfg.dataDir, "workspaces", "run-"+fx.Run.String()), filepath.Join(a.cfg.dataDir, "workspaces", "change-"+fx.Change.String())}
	for _, p := range paths {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "original"), []byte("historical contents"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	purgeDeployment(t, a, "main")
	f.markStarted("apptown-env-game-main")
	f.markStarted(docker.PreviewChangeName(fx.Change))
	return a, f, paths
}

// runCleanup runs a deleted project's queued cleanup round, as the queue
// would after the deletion committed.
func runCleanup(t *testing.T, a *App, projectID ids.ID, round int) {
	t.Helper()
	runQueuedJobForTest(t, a, projectlifecycle.CleanupJob, projectlifecycle.CleanupKey(projectID, round))
}

// deleteProject asks for the project's deletion as boss, then moves the
// AppTown's clock past the grace period and runs the deletion pass, running
// each take-down it admits, until the project is deleted or the deletion
// stalls. It returns where the deletion stands.
func deleteProject(t *testing.T, a *App, slug string) projectlifecycle.Status {
	t.Helper()
	return deleteProjectAs(t, a, "boss", slug)
}

func deleteProjectAs(t *testing.T, a *App, who, slug string) projectlifecycle.Status {
	t.Helper()
	out := authzCall(a.Handler(), who, "DELETE", "/api/v1/projects/"+slug, `{"confirm":"`+slug+`"}`)
	if out.Code != 202 {
		t.Fatalf("delete %s: %d %s", slug, out.Code, out.Body.String())
	}
	var st projectlifecycle.Status
	if err := json.Unmarshal(out.Body.Bytes(), &st); err != nil || st.State != project.Deleting {
		t.Fatalf("delete %s: %s %v", slug, out.Body.String(), err)
	}
	return passDeletion(t, a, st.ProjectID)
}

// passDeletion moves the clock past the grace period and runs the deletion
// pass until the project is deleted or the deletion stalls.
func passDeletion(t *testing.T, a *App, id ids.ID) projectlifecycle.Status {
	t.Helper()
	moveClock(a, projectlifecycle.DeletionGrace+time.Minute)
	ctx := context.Background()
	for range 20 {
		st, err := a.deletion.Status(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if st.State != project.Deleting || st.Stalled != "" {
			return st
		}
		if st.TakeDown != nil && st.TakeDown.State.Live() {
			if _, err := a.operations.RunHere(ctx, st.TakeDown.ID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := a.deletion.Pass(ctx, fmt.Sprintf("project/%d", id), nil); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("the deletion pass did not settle")
	return projectlifecycle.Status{}
}

// moveClock moves AppTown's clock forward.
func moveClock(a *App, by time.Duration) {
	a.cfg.clock.mu.Lock()
	a.cfg.clock.ahead += by
	a.cfg.clock.mu.Unlock()
}

// purgeProject purges a deleted project now, as boss, and runs the purge.
func purgeProject(t *testing.T, a *App, slug string) *operations.Operation {
	t.Helper()
	return purgeProjectAs(t, a, "boss", slug)
}

func purgeProjectAs(t *testing.T, a *App, who, slug string) *operations.Operation {
	t.Helper()
	out := authzCall(a.Handler(), who, "POST", "/api/v1/projects/"+slug+"/purge", "")
	if out.Code != 202 {
		t.Fatalf("purge %s: %d %s", slug, out.Code, out.Body.String())
	}
	var admitted httpapi.OperationAdmitted
	if err := json.Unmarshal(out.Body.Bytes(), &admitted); err != nil {
		t.Fatal(err)
	}
	op, err := a.operations.RunHere(context.Background(), admitted.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

// purgeWhenDue moves the clock past the project's kept period, runs the
// scheduled purge pass and the purge it admits.
func purgeWhenDue(t *testing.T, a *App, id ids.ID) *operations.Operation {
	t.Helper()
	ctx := context.Background()
	moveClock(a, projectlifecycle.DefaultKeptDays*24*time.Hour+time.Hour)
	if _, err := a.deletion.PurgePass(ctx, "", nil); err != nil {
		t.Fatal(err)
	}
	st, err := a.deletion.Status(ctx, id)
	if err != nil || st.Purge == nil {
		t.Fatalf("no purge admitted: %+v %v", st, err)
	}
	op, err := a.operations.RunHere(ctx, st.Purge.ID)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func purgeDeployment(t *testing.T, a *App, name string) {
	t.Helper()
	target, err := a.Deployment.Targets.Put(context.Background(), &deployment.Target{Name: "purge-" + name, Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Deployments.EnsureStanding(t.Context(), idstest.N(1), name, target.ID, (deployment.Attributes{}).WithDefaults()); err != nil {
		t.Fatal(err)
	}
}

// A purge whose records step fails leaves the project deleted with all its
// records, and its workspaces in place; the purge ends failed with what is
// left named, no purge receipt is written, and Restore stays closed. A
// failed purge changes no record, so each fault is tried in turn on one
// deleted project; after the first, the purge's earlier steps (backups,
// the hub) have already run, and the records step is the same.
func TestProjectPurgeFailurePreservesDatabaseAndExternalState(t *testing.T) {
	t.Parallel()
	a, f, paths := purgeFixture(t)
	before := purgeRemovalCounts(t, a)
	if st := deleteProject(t, a, "game"); st.State != project.Deleted {
		t.Fatalf("deletion: %+v", st)
	}
	for _, fault := range []string{"inventory", "middle participant", "root", "receipt error", "receipt suppressed"} {
		t.Run(fault, func(t *testing.T) {
			completionExec(t, a.DB, `DROP FUNCTION IF EXISTS refuse_project_receipt() CASCADE`)
			switch fault {
			case "inventory":
				dbtest.FailNextStatement(t, a.DB, "FROM deployments WHERE project_id=")
			case "middle participant":
				dbtest.FailNextStatement(t, a.DB, "DELETE FROM dispositions")
			case "root":
				dbtest.FailNextStatement(t, a.DB, "DELETE FROM projects")
			default:
				body := `RAISE EXCEPTION 'receipt refused';`
				if fault == "receipt suppressed" {
					body = `RETURN NULL;`
				}
				completionExec(t, a.DB, `CREATE FUNCTION refuse_project_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='project.deleted' THEN `+body+` END IF; RETURN NEW; END $$`)
				completionExec(t, a.DB, `CREATE TRIGGER refuse_project_receipt BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION refuse_project_receipt()`)
			}
			f.mu.Lock()
			callsBefore := len(f.calls)
			f.mu.Unlock()
			op := purgeProject(t, a, "game")
			if op.State != operations.Failed || !strings.Contains(op.Detail, "purge incomplete") || !strings.Contains(op.Detail, "the project's records") {
				t.Fatalf("%s: purge %s %q", fault, op.State, op.Detail)
			}
			if l, err := a.Projects.Lifecycle(context.Background(), idstest.N(1)); err != nil || l.State != project.Deleted {
				t.Fatalf("a failed purge moved the project: %+v %v", l, err)
			}
			if after := purgeRemovalCounts(t, a); after != before {
				t.Fatalf("failed purge changed Work rows: %+v, want %+v", after, before)
			}
			// sql-guarantee: a remembered (engine-internal) integration row has no owner read; the attachment reads hide rows without provenance.
			completionCount(t, a.DB, 1, `SELECT COUNT(*) FROM project_integrations WHERE project_id='00000000-0001-7000-8000-000000000000'`)
			if n := slotOverrides(t, a, "game", "purge"); n != 1 {
				t.Fatalf("project slot: %d", n)
			}
			if n := len(ledgerEntries(t, a, ids.Nil, "project.deleted")) + len(ledgerEntries(t, a, idstest.N(1), "project.purged")); n != 0 {
				t.Fatalf("%d purge receipts after a failed purge", n)
			}
			// The workspaces go with the cleanup the records' removal queues.
			for _, p := range paths[1:] {
				b, err := os.ReadFile(filepath.Join(p, "original"))
				if err != nil || string(b) != "historical contents" {
					t.Fatalf("external cleanup before the records went: %s %s %v", p, b, err)
				}
			}
			f.mu.Lock()
			calls := len(f.calls) - callsBefore
			f.mu.Unlock()
			if calls != 0 {
				t.Fatalf("%d Docker calls by a failed purge", calls)
			}
			if w := authzCall(a.Handler(), "boss", "POST", "/api/v1/projects/game/restore", ""); w.Code != 409 || !strings.Contains(w.Body.String(), "purge has started") {
				t.Fatalf("restore after a failed purge: %d %s", w.Code, w.Body.String())
			}
			if _, err := conversation.ReserveProject(context.Background(), a.Console, a.DB, idstest.N(1)); err == nil {
				t.Fatal("a deleted project's chats were admitted")
			}
		})
	}
}

// A purge removes the records in one transaction with the receipt, then
// the queued cleanup removes the workspaces while the name stays held;
// usage receipts and other projects stay as they were.
func TestProjectPurgeReceiptPrecedesCleanupAndPreservesAccounting(t *testing.T) {
	t.Parallel()
	a, f, paths := purgeFixture(t)
	fx := fixtureWork(t, a)
	expected := purgeRemovalCounts(t, a)
	checked := false
	if st := deleteProject(t, a, "game"); st.State != project.Deleted {
		t.Fatalf("deletion: %+v", st)
	}
	f.reply = func(args []string) (string, error, bool) {
		if !checked {
			checked = true
			if projectExists(t, a, idstest.N(1)) {
				t.Fatal("project still listed while its cleanup runs")
			}
			if _, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "game", Name: "Replacement", Actor: "boss"}); err == nil {
				t.Fatal("name reused while old cleanup still owns paths")
			}

			pending := 0
			for _, e := range ledgerEntries(t, a, ids.Nil, "project.deleted") {
				if e.Subject == "game" && exactText(e.Payload, "cleanup") == "pending" {
					pending++
				}
			}
			if pending != 1 {
				t.Fatalf("%d pending deletion receipts before cleanup", pending)
			}
		}
		return "", nil, false
	}
	if op := purgeProject(t, a, "game"); op.State != operations.Succeeded || checked {
		t.Fatalf("purge: %s %q; cleanup ran inline=%v", op.State, op.Detail, checked)
	}
	runCleanup(t, a, idstest.N(1), 0)
	if !checked {
		t.Fatal("the queued cleanup did not run")
	}
	for _, p := range paths {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("workspace remains: %s %v", p, err)
		}
	}
	for _, table := range []string{"runs", "changes", "steps", "artifacts", "run_events"} {
		// sql-guarantee: the purge leaves no Work row anywhere; no owner read reaches rows of a purged project.
		completionCount(t, a.DB, 0, `SELECT COUNT(*) FROM `+table)
	}
	for _, table := range []string{"project_integrations", "model_slots"} {
		// sql-guarantee: orphaned rows of a purged project are invisible to the owners' reads, which join the project.
		completionCount(t, a.DB, 0, `SELECT COUNT(*) FROM `+table+` WHERE project_id='00000000-0001-7000-8000-000000000000'`)
	}
	// The usage receipt recorded above survives the purge as itself: still
	// the project's only row, with its run, kind, actor and total unchanged.
	// A rollup could agree with an altered row plus an added one.
	// sql-guarantee: usage reads are rollups; none lists the stored receipt rows, and the purge promises those rows themselves are left intact.
	completionCount(t, a.DB, 1, `SELECT COUNT(*) FROM token_usage WHERE project_id='00000000-0001-7000-8000-000000000000'`)
	// sql-guarantee: the same receipt by identity, as recorded before the purge.
	completionCount(t, a.DB, 1, `SELECT COUNT(*) FROM token_usage WHERE project_id='00000000-0001-7000-8000-000000000000' AND run_id='`+fx.Run.String()+`' AND root_run_id='`+fx.Run.String()+`' AND kind='agent' AND actor='owner' AND total=123`)
	// The project's own spending, not the run's: still attributed to it.
	if report, err := usage.New(a.DB).Summary(context.Background(), 1, idstest.N(1), ""); err != nil || report.Totals.Total != 123 {
		t.Fatalf("purged project's spending: %+v %v", report.Totals, err)
	}
	// Its backups went with it: the final copy main's take-down made, and
	// the ordinary set no newer set would ever have pruned.
	if kept, err := dataprotection.NewBackups(a.DB).ForDeployment(context.Background(), idstest.N(1), "main", 10); err != nil || len(kept) != 0 {
		t.Fatalf("purged project's backups: %+v %v", kept, err)
	}
	if cells, err := model.New(a.DB).Cells(context.Background()); err != nil || cells["purge"]["default"] != "provider/x" {
		t.Fatalf("instance slot: %v %v", cells["purge"], err)
	}
	if n := slotOverrides(t, a, "open", "purge"); n != 1 {
		t.Fatalf("other project's slot: %d", n)
	}
	// sql-guarantee: a remembered (engine-internal) integration row has no owner read.
	completionCount(t, a.DB, 1, `SELECT COUNT(*) FROM project_integrations WHERE project_id='00000000-0002-7000-8000-000000000000'`)
	if !projectExists(t, a, idstest.N(2)) {
		t.Fatal("purge removed another project")
	}
	receipts := ledgerEntries(t, a, ids.Nil, "project.deleted")
	if len(receipts) != 1 || receipts[0].Subject != "game" {
		t.Fatalf("deletion receipts: %+v", receipts)
	}
	var counts workflow.RemovalCounts
	if err := json.Unmarshal(receipts[0].Payload, &counts); err != nil {
		t.Fatal(err)
	}
	if counts != expected {
		t.Fatalf("receipt counts %+v", counts)
	}
	if purged := ledgerEntries(t, a, idstest.N(1), "project.purged"); len(purged) != 1 || purged[0].Actor != "boss" {
		t.Fatalf("purge receipts: %+v", purged)
	}
	if _, err := conversation.ReserveProject(context.Background(), a.Console, a.DB, idstest.N(1)); err == nil {
		t.Fatal("stale request admitted to purged project")
	}
}

func purgeRemovalCounts(t *testing.T, a *App) workflow.RemovalCounts {
	t.Helper()
	var counts workflow.RemovalCounts
	// sql-guarantee: an independent count of the Work rows, held against the counts the purge's own inventory (workflow.ProjectRemovalIn) writes into its receipt.
	if err := a.DB.QueryRow(`SELECT COUNT(*) FROM runs WHERE project_id='00000000-0001-7000-8000-000000000000'`).Scan(&counts.Runs); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: an independent count of the Work rows, held against the counts the purge's own inventory writes into its receipt
	if err := a.DB.QueryRow(`SELECT COUNT(*) FROM changes WHERE project_id='00000000-0001-7000-8000-000000000000'`).Scan(&counts.Changes); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: an independent count of the Work rows, held against the counts the purge's own inventory writes into its receipt
	if err := a.DB.QueryRow(`SELECT COUNT(*) FROM steps WHERE run_id IN (SELECT id FROM runs WHERE project_id='00000000-0001-7000-8000-000000000000')`).Scan(&counts.Steps); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: an independent count of the Work rows, held against the counts the purge's own inventory writes into its receipt
	if err := a.DB.QueryRow(`SELECT COUNT(*) FROM run_events WHERE run_id IN (SELECT id FROM runs WHERE project_id='00000000-0001-7000-8000-000000000000')`).Scan(&counts.Events); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: an independent count of the Work rows, held against the counts the purge's own inventory writes into its receipt
	if err := a.DB.QueryRow(`SELECT COUNT(*) FROM artifacts WHERE run_id IN (SELECT id FROM runs WHERE project_id='00000000-0001-7000-8000-000000000000')`).Scan(&counts.Artifacts); err != nil {
		t.Fatal(err)
	}
	return counts
}

// A deletion takes the project's chat block before it marks anything: a
// chat being opened or in use refuses it; once deleting, no chat is
// opened, identified or used, and the block holds while the project is
// deleting or deleted; cancelling releases it.
func TestProjectDeletionConsoleAdmissionAndLiveRefusal(t *testing.T) {
	t.Parallel()
	a, _, _ := purgeFixture(t)
	h := a.Handler()
	releaseCreate, err := conversation.ReserveProject(context.Background(), a.Console, a.DB, idstest.N(1))
	if err != nil {
		t.Fatal(err)
	}
	if w := authzCall(h, "boss", "DELETE", "/api/v1/projects/game", `{"confirm":"game"}`); w.Code != 409 {
		t.Fatalf("deletion while a chat is being opened: %d %s", w.Code, w.Body.String())
	}
	releaseCreate()
	m := withLabel(consoleMetaFor("purge-chat", idstest.N(1), ids.Nil), conversation.SlugLabel, "game")
	m.Token = "test-token"
	s := a.Console.Restore(m)
	s.Touch() // the chat is mid-conversation
	if w := authzCall(h, "boss", "DELETE", "/api/v1/projects/game", `{"confirm":"game"}`); w.Code != 409 {
		t.Fatalf("deletion with an active chat: %d %s", w.Code, w.Body.String())
	}
	if l, err := a.Projects.Lifecycle(context.Background(), idstest.N(1)); err != nil || l.State != project.Active {
		t.Fatalf("a refused deletion changed the project: %+v %v", l, err)
	}
	s.SetLastUsed(time.Now().Add(-time.Hour))
	if w := authzCall(h, "boss", "DELETE", "/api/v1/projects/game", `{"confirm":"game"}`); w.Code != 202 {
		t.Fatalf("deletion with an idle chat: %d %s", w.Code, w.Body.String())
	}
	if _, ok := a.Console.Identity(s.Token); ok || a.Console.Annotate(context.Background(), s.ID, "x", "1") == nil {
		t.Fatal("chat activity admitted while deleting")
	}
	if release, err := conversation.ReserveProject(context.Background(), a.Console, a.DB, idstest.N(1)); err == nil {
		release()
		t.Fatal("a chat was opened on a deleting project")
	}
	// The block never holds the application mutex across SQL or cleanup.
	stopped := make(chan struct{})
	go func() { a.StopConsoles(context.Background()); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("the deletion's chat block blocked shutdown")
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/projects/game/deletion/cancel", ""); w.Code != 200 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	release, err := conversation.ReserveProject(context.Background(), a.Console, a.DB, idstest.N(1))
	if err != nil {
		t.Fatalf("a cancelled deletion kept chats blocked: %v", err)
	}
	release()
}

// A console request refused for a wrong token neither counts as activity
// nor blocks the project's deletion.
func TestConsoleRejectedTokenDoesNotBlockProjectDeletion(t *testing.T) {
	t.Parallel()
	a, _, _ := purgeFixture(t)
	used := time.Now().Add(-time.Hour)
	m := consoleMetaFor("private", idstest.N(1), ids.Nil)
	m.Token = "secret"
	s := a.Console.Restore(m)
	s.SetLastUsed(used)
	for _, handler := range []func(http.ResponseWriter, *http.Request){httpapi.NewConsolePreview(a.Console, a.Preview, a.DB, a.cfg.publicHost, nil).ConsolePreview, httpapi.NewConsoleInvocation(a.Console, a.Projects, a.Ways, a.Work, a.changeActions, a.DB, a.cfg.publicHost, nil).ConsoleInvoke, httpapi.NewConsoleChanges(a.Console, a.Work, a.changeActions, a.DB).ConsoleLand, httpapi.NewConsoleChanges(a.Console, a.Work, a.changeActions, a.DB).ConsoleRecheck} {
		req := httptest.NewRequest("POST", "/api/v1/console/private/preview", nil)
		req.SetPathValue("sid", s.ID)
		req.Header.Set("X-AppTown-Session-Token", "wrong")
		out := httptest.NewRecorder()
		handler(out, req)
		if out.Code != 403 || !s.LastUsed().Equal(used) {
			t.Fatalf("rejected token changed activity: %d %v", out.Code, s.LastUsed())
		}
	}
	if w := authzCall(a.Handler(), "boss", "DELETE", "/api/v1/projects/game", `{"confirm":"game"}`); w.Code != 202 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
}

func TestProjectDeletionTakesEachDeploymentDownAndPurgesNothingByName(t *testing.T) {
	t.Parallel()
	a, f, _ := purgeFixture(t)
	purgeDeployment(t, a, "staging")
	// Containers under the project's names that no deployment recorded
	// creating, and another project's whose name extends this one's.
	unrecorded := []string{"apptown-env-game-main", "apptown-env-game-staging", "apptown-svc-env-game-main-db"}
	foreign := []string{"apptown-env-game-tools-main", "apptown-svc-env-game-tools-main-db"}
	for _, name := range append(unrecorded, foreign...) {
		f.markStarted(name)
	}
	if st := deleteProject(t, a, "game"); st.State != project.Deleted {
		t.Fatalf("deletion: %+v", st)
	}
	purgeProject(t, a, "game")
	runCleanup(t, a, idstest.N(1), 0)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, name := range append(unrecorded, foreign...) {
		if !f.started[name] {
			t.Errorf("a container no deployment recorded was removed: %s", name)
		}
	}
	for _, call := range f.calls {
		if (call[0] == "volume" && call[1] == "rm" && !strings.Contains(call[2], "preview")) || (call[0] == "rm" && strings.Contains(call[len(call)-1], "env-game")) {
			t.Errorf("project deletion removed by name: %v", call)
		}
	}
	// Each deployment went through the take-down any deployment gets,
	// main last.
	receipts, err := audit.Recent(context.Background(), a.DB, audit.Filter{ProjectID: idstest.N(1), KindPrefix: "deployment.torn-down"})
	if err != nil {
		t.Fatal(err)
	}
	var down []string
	for _, r := range receipts {
		down = append([]string{r.Subject}, down...) // Recent is newest first
	}
	if !slices.Equal(down, []string{"staging", "main"}) {
		t.Fatalf("taken down: %v", down)
	}
}

// Project deletion takes main down the way any deployment is taken down:
// the container AppTown recorded goes without -v, and a container and
// volume it did not record stay, whatever their names.
func TestProjectDeletionRemovesOnlyWhatTheDeploymentRecorded(t *testing.T) {
	t.Parallel()
	a, f, _ := purgeFixture(t)
	env, err := a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Deployment.Deployments.OwnerOf(env).Created(context.Background(), deploy.Owned{Kind: "container", ID: "apptown-env-game-main", Name: "apptown-env-game-main"}); err != nil {
		t.Fatal(err)
	}
	need := "apptown-svc-env-game-main-db"
	f.markStarted(need)
	f.mu.Lock()
	f.objects["volume apptown-need-env-game-main-db"] = true
	f.mu.Unlock()
	if st := deleteProject(t, a, "game"); st.State != project.Deleted {
		t.Fatalf("deletion: %+v", st)
	}
	purgeProject(t, a, "game")
	runCleanup(t, a, idstest.N(1), 0)
	f.mu.Lock()
	defer f.mu.Unlock()
	removed := false
	for _, call := range f.calls {
		if call[0] == "rm" && call[len(call)-1] == "apptown-env-game-main" {
			removed = true
			if slices.Contains(call, "-v") {
				t.Errorf("the recorded container went with its unrecorded volumes: %v", call)
			}
		}
		if call[0] == "rm" && call[len(call)-1] == need {
			t.Errorf("an unrecorded need container was removed: %v", call)
		}
		if call[0] == "volume" && call[1] == "rm" && !strings.Contains(call[2], "preview") {
			t.Errorf("an unrecorded volume was removed: %v", call)
		}
	}
	if !removed {
		t.Error("the recorded container stayed")
	}
}

// Recorded data whose final backup cannot be taken stalls the project's
// deletion at that deployment: nothing of it is removed, the project stays
// deleting, and the stall names why.
func TestProjectDeletionIsRefusedWhenAFinalBackupFallsShort(t *testing.T) {
	t.Parallel()
	a, f, paths := purgeFixture(t)
	env, err := a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
	if err != nil {
		t.Fatal(err)
	}
	// Postgres's data volume is recorded; no running container on the list
	// mounts it, so nothing can copy it.
	if err := a.Deployment.Deployments.OwnerOf(env).Created(context.Background(), deploy.Owned{Kind: "volume", ID: "apptown-need-env-game-main-postgres", Data: true, Need: "postgres"}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.objects["volume apptown-need-env-game-main-postgres"] = true
	f.mu.Unlock()
	st := deleteProject(t, a, "game")
	if st.State != project.Deleting || !strings.Contains(st.Stalled, "the take-down of main") || !strings.Contains(st.Stalled, "nothing was removed") {
		t.Fatalf("deletion: %+v", st)
	}
	if _, err := a.Projects.BySlug(context.Background(), "game"); err != nil {
		t.Fatalf("the stalled deletion removed the project: %v", err)
	}
	if owned, err := a.Deployment.Deployments.Owned(context.Background(), env.ID); err != nil || len(owned) != 1 {
		t.Fatalf("the deployment's list: %+v %v", owned, err)
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("a stalled deletion removed %s: %v", p, err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if call[0] == "rm" || (call[0] == "volume" && call[1] == "rm") {
			t.Errorf("a stalled deletion removed something: %v", call)
		}
	}
}

// A deletion's cleanup is recorded with it and survives failure: a Docker
// error is retried, exhausted retries wait for a person without releasing
// the name, and a deliberate retry finishes the job. The address remains
// reserved even after cleanup so old links never identify another project.
func TestProjectCleanupSurvivesFailureAndHoldsItsName(t *testing.T) {
	t.Parallel()
	a, f, paths := purgeFixture(t)
	fx := fixtureWork(t, a)
	var broken atomic.Bool
	broken.Store(true)
	f.reply = func(args []string) (string, error, bool) {
		if broken.Load() && len(args) > 1 && args[0] == "rm" && args[len(args)-1] == docker.PreviewChangeName(fx.Change) {
			return "permission denied", errors.New("exit status 1"), true
		}
		return "", nil, false
	}
	h := a.Handler()
	deleteProject(t, a, "game")
	purgeProject(t, a, "game")
	cleanup := func() project.Deletion {
		t.Helper()
		w := authzCall(h, "boss", "GET", "/api/v1/project-cleanups", "")
		var list struct{ Cleanups []project.Deletion }
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 {
			t.Fatalf("cleanups: %d %s %v", w.Code, w.Body.String(), err)
		}
		for _, d := range list.Cleanups {
			if d.ProjectID == idstest.N(1) {
				return d
			}
		}
		t.Fatalf("no cleanup for the deleted project: %s", w.Body.String())
		return project.Deletion{}
	}
	nameHeld := func() bool {
		t.Helper()
		_, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "game", Name: "Replacement", Actor: "boss"})
		if err != nil && !errors.Is(err, project.ErrSlugTaken) {
			t.Fatalf("create: %v", err)
		}
		return err != nil
	}
	// The process could stop here: the records are gone, the cleanup is known.
	if d := cleanup(); d.State != project.CleanupPending || d.Attempts != 0 || !nameHeld() {
		t.Fatalf("after commit: %+v", d)
	}
	for attempt := 1; attempt <= projectlifecycle.CleanupAttempts; attempt++ {
		runCleanup(t, a, idstest.N(1), 0)
	}
	d := cleanup()
	if d.State != project.CleanupNeedsAttention || !strings.Contains(d.Detail, "permission denied") || d.Attempts != projectlifecycle.CleanupAttempts {
		t.Fatalf("exhausted cleanup: %+v", d)
	}
	if !nameHeld() {
		t.Fatal("a cleanup waiting for a person released the name")
	}
	// The rest of the plan was carried out; the failing container is named.
	for _, p := range paths {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("workspace remains: %s %v", p, err)
		}
	}

	if w := authzCall(h, "owner", "POST", "/api/v1/project-cleanups/00000000-0001-7000-8000-000000000000/retry", ""); w.Code != 403 {
		t.Fatalf("a non-admin retried a cleanup: %d %s", w.Code, w.Body.String())
	}
	broken.Store(false)
	if w := authzCall(h, "boss", "POST", "/api/v1/project-cleanups/00000000-0001-7000-8000-000000000000/retry", ""); w.Code != 200 {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/project-cleanups/00000000-0001-7000-8000-000000000000/retry", ""); w.Code != 409 {
		t.Fatalf("a pending cleanup was restarted again: %d %s", w.Code, w.Body.String())
	}
	runCleanup(t, a, idstest.N(1), 1)
	if d := cleanup(); d.State != project.CleanupDone || d.Round != 1 || d.FinishedAt == nil {
		t.Fatalf("retried cleanup: %+v", d)
	}
	if !nameHeld() {
		t.Fatal("cleanup completion reassigned a permanent project address")
	}
}

// The queue can give up on a cleanup's job before the cleanup records its last
// attempt: an attempt whose settlement failed still spends one of the queue's
// tries. A cleanup left pending with no job running is shown as needing
// attention and can be retried to complete the promised resource removal.
func TestProjectCleanupTheQueueGaveUpOnCanBeRetried(t *testing.T) {
	t.Parallel()
	a, f, _ := purgeFixture(t)
	fx := fixtureWork(t, a)
	f.reply = func(args []string) (string, error, bool) {
		if len(args) > 1 && args[0] == "rm" && args[len(args)-1] == docker.PreviewChangeName(fx.Change) {
			return "permission denied", errors.New("exit status 1"), true
		}
		return "", nil, false
	}
	h := a.Handler()
	deleteProject(t, a, "game")
	purgeProject(t, a, "game")
	dbtest.FailNextStatement(t, a.DB, "UPDATE project_deletions SET attempts")
	for range projectlifecycle.CleanupAttempts {
		runCleanup(t, a, idstest.N(1), 0)
	}
	w := authzCall(h, "boss", "GET", "/api/v1/project-cleanups", "")
	var list struct{ Cleanups []project.Deletion }
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Cleanups) != 1 {
		t.Fatalf("cleanups: %d %s %v", w.Code, w.Body.String(), err)
	}
	if d := list.Cleanups[0]; d.State != project.CleanupNeedsAttention || !strings.Contains(d.Detail, "the queue stopped trying") {
		t.Fatalf("a cleanup the queue gave up on: %+v", d)
	}
	if retry := authzCall(h, "boss", "POST", "/api/v1/project-cleanups/00000000-0001-7000-8000-000000000000/retry", ""); retry.Code != 200 {
		t.Fatalf("retry: %d %s", retry.Code, retry.Body.String())
	}
	// While the new round's job is live, a second retry waits for it.
	if again := authzCall(h, "boss", "POST", "/api/v1/project-cleanups/00000000-0001-7000-8000-000000000000/retry", ""); again.Code != 409 {
		t.Fatalf("retry while running: %d %s", again.Code, again.Body.String())
	}
}

// A deleted project's preview stacks go with it: the preview's own services
// (a PostgreSQL sidecar) are removed before the cleanup counts as done.
func TestProjectCleanupRemovesPreviewStacks(t *testing.T) {
	t.Parallel()
	a, f, _ := purgeFixture(t)
	fx := fixtureWork(t, a)
	sidecar := docker.Current.Service(docker.ChangeStackID(fx.Change), "postgres")
	f.markStarted(sidecar)
	f.reply = func(args []string) (string, error, bool) {
		call := strings.Join(args, " ")
		if strings.Contains(call, "label=apptown.class=service") && strings.Contains(call, "label=apptown.owner=pvsnap-change-"+fx.Change.String()) {
			return sidecar + "\n", nil, true
		}
		return "", nil, false
	}
	deleteProject(t, a, "game")
	purgeProject(t, a, "game")
	runCleanup(t, a, idstest.N(1), 0)
	d, err := a.Projects.Deletion(context.Background(), idstest.N(1))
	if err != nil {
		t.Fatal(err)
	}
	if d.State == project.CleanupDone && f.isStarted(sidecar) {
		t.Fatal("cleanup marked done with the Change preview's PostgreSQL container still running")
	}
}

// A project is deleted only once each of its deployments has been taken
// down the way any deployment is; one AppTown may not take down (a
// Portainer stack it did not record creating) stalls the deletion, named,
// after the ones before it went, and main, taken down last, still serves.
func TestProjectDeletionWaitsForDeploymentsOnOtherTargets(t *testing.T) {
	t.Parallel()
	a, _, _ := purgeFixture(t)
	purgeDeployment(t, a, "staging")
	target, err := a.Deployment.Targets.Put(context.Background(), &deployment.Target{Name: "remote", Kind: deployment.KindPortainer, Origin: deployment.OriginAttached, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Deployments.EnsureStanding(t.Context(), idstest.N(1), "remote", target.ID, (deployment.Attributes{}).WithDefaults()); err != nil {
		t.Fatal(err)
	}
	st := deleteProject(t, a, "game")
	if st.State != project.Deleting || !strings.HasPrefix(st.Stalled, "the take-down of remote") || !slices.Equal(st.Remaining, []string{"remote", "main"}) {
		t.Fatalf("deletion with a deployment elsewhere: %+v", st)
	}
	if _, err := a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "staging"); err == nil {
		t.Fatal("staging, taken down first, still stands")
	}
	if _, err := a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main"); err != nil {
		t.Fatalf("main was taken down before the stall: %v", err)
	}
	// The next pass does not try again blind.
	if again := passDeletion(t, a, idstest.N(1)); again.Stalled != st.Stalled {
		t.Fatalf("a stalled deletion moved on: %+v", again)
	}
	if w := authzCall(a.Handler(), "boss", "POST", "/api/v1/projects/game/deletion/cancel", ""); w.Code != 409 {
		t.Fatalf("cancel after a take-down: %d %s", w.Code, w.Body.String())
	}
}

// A deleted project's previews' build images go with it; one a container
// still uses stays, without failing the cleanup. Its deployments' images
// go with their take-down when recorded, never by name.
func TestProjectCleanupRemovesItsBuildImages(t *testing.T) {
	t.Parallel()
	a, f, _ := purgeFixture(t)
	fx := fixtureWork(t, a)
	purgeDeployment(t, a, "staging")
	createReadTestProject(t, a, "game-main")
	owned := []string{"apptown-preview-build-apptown-preview-change-" + fx.Change.String() + ":latest", "apptown-preview-build-apptown-preview-run-" + fx.Run.String() + "-web:latest"}
	deployments := []string{"apptown-preview-build-apptown-env-game-main:latest", "apptown-preview-build-apptown-env-game-staging-g2-web:latest"}
	inUse := "apptown-preview-build-apptown-preview-run-" + fx.Run.String() + ":latest"
	// game-main's web deployment; game's main deployment's web part would
	// carry the same name, and another project's claim wins.
	foreign := "apptown-preview-build-apptown-env-game-main-web:latest"
	f.reply = func(args []string) (string, error, bool) {
		call := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(call, "image ls") && strings.Contains(call, "reference=apptown-preview-build-apptown-env-game-main "):
			return deployments[0] + "\n" + foreign, nil, true
		case strings.HasPrefix(call, "image ls") && strings.Contains(call, "reference=apptown-preview-build-apptown-env-game-staging "):
			return deployments[1], nil, true
		case strings.HasPrefix(call, "image ls") && strings.Contains(call, "reference=apptown-preview-build-apptown-preview-change-"+fx.Change.String()+" "):
			return owned[0], nil, true
		case strings.HasPrefix(call, "image ls") && strings.Contains(call, "reference=apptown-preview-build-apptown-preview-run-"+fx.Run.String()+" "):
			return owned[1] + "\n" + inUse, nil, true
		case strings.HasPrefix(call, "image ls"):
			return "", nil, true
		case call == "rmi "+inUse:
			return "Error response from daemon: conflict: unable to remove repository reference \"" + inUse + "\" (must force) - container 0123 is using its referenced image 4567", errors.New("exit status 1"), true
		}
		return "", nil, false
	}
	deleteProject(t, a, "game")
	purgeProject(t, a, "game")
	runCleanup(t, a, idstest.N(1), 0)
	if d, err := a.Projects.Deletion(context.Background(), idstest.N(1)); err != nil || d.State != project.CleanupDone {
		t.Fatalf("cleanup: %+v %v", d, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	removed := map[string]bool{}
	for _, call := range f.calls {
		if call[0] == "rmi" {
			if slices.Contains(call, "-f") || slices.Contains(call, "--force") {
				t.Errorf("an image was forced: %v", call)
			}
			removed[call[len(call)-1]] = true
		}
	}
	for _, ref := range owned {
		if !removed[ref] {
			t.Errorf("the project's image %s was not removed", ref)
		}
	}
	for _, ref := range append(deployments, foreign) {
		if removed[ref] {
			t.Errorf("an image no take-down recorded was removed by name: %s", ref)
		}
	}
}

// Running work refuses a project's deletion before any of its deployments
// is taken down: the project stays active with its chats open, and nothing
// of main is removed.
func TestProjectDeletionWithRunningWorkTakesNothingDown(t *testing.T) {
	t.Parallel()
	a, f, _ := purgeFixtureState(t, true)
	env, err := a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Deployment.Deployments.OwnerOf(env).Created(context.Background(), deploy.Owned{Kind: "container", ID: "apptown-env-game-main", Name: "apptown-env-game-main"}); err != nil {
		t.Fatal(err)
	}
	if out := authzCall(a.Handler(), "boss", "DELETE", "/api/v1/projects/game", `{"confirm":"game"}`); out.Code != 409 {
		t.Fatalf("delete with running work: %d %s", out.Code, out.Body.String())
	}
	if l, err := a.Projects.Lifecycle(context.Background(), idstest.N(1)); err != nil || l.State != project.Active {
		t.Fatalf("a refused deletion changed the project: %+v %v", l, err)
	}
	release, err := conversation.ReserveProject(context.Background(), a.Console, a.DB, idstest.N(1))
	if err != nil {
		t.Fatalf("a refused deletion kept chats blocked: %v", err)
	}
	release()
	if _, err := a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main"); err != nil {
		t.Fatalf("main was taken down while the project stayed: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if call[0] == "rm" {
			t.Errorf("a refused deletion removed %v", call)
		}
	}
}

// The deletion routes ask the deletion owner: a maintainer may not delete,
// cancel, restore or purge; a project owner may delete, cancel and
// restore, but only an instance administrator purges now.
func TestProjectDeletionRoutesAskTheDeletionOwner(t *testing.T) {
	t.Parallel()
	a, _, _ := purgeFixture(t)
	h := a.Handler()
	for _, call := range []struct{ method, path, body string }{
		{"DELETE", "/api/v1/projects/game", `{"confirm":"game"}`},
		{"POST", "/api/v1/projects/game/deletion/cancel", ""},
		{"POST", "/api/v1/projects/game/restore", ""},
		{"POST", "/api/v1/projects/game/purge", ""},
	} {
		if w := authzCall(h, "owner", call.method, call.path, call.body); w.Code != 403 {
			t.Errorf("a maintainer: %s %s: %d %s", call.method, call.path, w.Code, w.Body.String())
		}
	}
	a.addGrant("user", "owner", access.RoleProjectOwner, "", access.ScopeProject, idstest.N(1).String(), "fixture", "boss", "")
	if w := authzCall(h, "owner", "DELETE", "/api/v1/projects/game", `{"confirm":"game"}`); w.Code != 202 {
		t.Fatalf("a project owner deletes: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "owner", "POST", "/api/v1/projects/game/deletion/cancel", ""); w.Code != 200 {
		t.Fatalf("a project owner cancels: %d %s", w.Code, w.Body.String())
	}
	if st := deleteProjectAs(t, a, "owner", "game"); st.State != project.Deleted {
		t.Fatalf("deletion: %+v", st)
	}
	if w := authzCall(h, "owner", "POST", "/api/v1/projects/game/purge", ""); w.Code != 403 {
		t.Fatalf("a project owner purged now: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "owner", "POST", "/api/v1/projects/game/restore", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"active"`) {
		t.Fatalf("a project owner restores: %d %s", w.Code, w.Body.String())
	}
}

// Over HTTP, a project that is being deleted or is deleted refuses new
// work with the lifecycle's refusal, and opens no chat. Each request is one
// the active project accepts (the active case shows it), so the refusal is
// the lifecycle's and not a missing input's. Every door is in its owner's
// table: Work's (TestEveryWorkDoorAsksTheProjectsLifecycle), the
// deployments' (TestDeploymentDoorsAskTheProjectsLifecycle) and evals'.
func TestEveryWorkDoorRefusesAProjectThatIsNotActive(t *testing.T) {
	t.Parallel()
	// The settings edits send the revision they started from, read when
	// they are made: on an active project each door's request is accepted
	// in turn, so each is a request the refusal below is a refusal of.
	revision := func(a *App) int64 { return a.projectByID(context.Background(), idstest.N(1)).SettingsRevision }
	doors := []struct {
		method, path string
		body         func(*App) string
		active       int
	}{
		{"POST", "/api/v1/projects/game/changes", func(*App) string { return `{"type":"feature","title":"more"}` }, 201},
		{"PATCH", "/api/v1/projects/game", func(a *App) string { return fmt.Sprintf(`{"name":"Renamed","expectedRevision":%d}`, revision(a)) }, 200},
	}
	// Each state has one AppTown: a refused door changes nothing, so every
	// door meets the same project.
	for _, state := range []string{project.Active, project.Deleting, project.Deleted} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			a, _, _ := purgeFixture(t)
			h := a.Handler()
			switch state {
			case project.Deleting:
				if w := authzCall(h, "boss", "DELETE", "/api/v1/projects/game", `{"confirm":"game"}`); w.Code != 202 {
					t.Fatalf("delete: %d %s", w.Code, w.Body.String())
				}
			case project.Deleted:
				if st := deleteProject(t, a, "game"); st.State != project.Deleted {
					t.Fatalf("delete: %+v", st)
				}
			}
			for _, door := range doors {
				door.path = strings.ReplaceAll(door.path, "{change}", fixtureWork(t, a).Change.String())
				w := authzCall(h, "boss", door.method, door.path, door.body(a))
				if state == project.Active {
					if w.Code != door.active {
						t.Errorf("active: %s %s: %d %s", door.method, door.path, w.Code, w.Body.String())
					}
					continue
				}
				if w.Code != 409 || !strings.Contains(w.Body.String(), "takes no new work") {
					t.Errorf("%s %s: %d %s", door.method, door.path, w.Code, w.Body.String())
				}
			}
			if state == project.Active {
				return
			}
			if release, err := conversation.ReserveProject(context.Background(), a.Console, a.DB, idstest.N(1)); err == nil {
				release()
				t.Error("a chat was opened")
			}
		})
	}
}

// Marking a deletion and starting a run on an existing Change race on the
// project's row: exactly one side refuses. Either the run is admitted and
// the deletion is refused for running work, or the project is deleting and
// the run is refused.
//
// Kills: a run admitted into a project the deletion already counted as
// idle; both refusing.
func TestMarkingADeletionAndStartingARunOneSideRefuses(t *testing.T) {
	t.Parallel()
	for i := range 4 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			a := agentTestApp(t, Config{DockerClient: newFakeDocker()})
			testAdministrator(t, a, "boss")
			p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "p", Name: "p", Actor: "boss"})
			if err != nil {
				t.Fatal(err)
			}
			if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
				t.Fatal(err)
			}
			defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "agent", Version: 1}, Spec: schema.Spec{
				Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "build", Uses: "agent/pi", With: map[string]any{"prompt": "x"}}}}},
			}})
			a.Work.Stop()
			ctx := asPerson("boss")
			change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Title: "work"})
			if err != nil {
				t.Fatal(err)
			}
			var runErr, deleteErr error
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, runErr = a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "agent"})
			}()
			go func() { defer wg.Done(); _, deleteErr = a.deletion.Request(ctx, p.ID) }()
			wg.Wait()
			admitted := runErr == nil || errors.Is(runErr, workflow.ErrDispatchStopped)
			switch {
			case admitted && deleteErr == nil:
				t.Fatal("a run was admitted and the project marked deleting")
			case !admitted && !errors.Is(runErr, project.ErrNotActive):
				t.Fatalf("the run was refused for another reason: %v", runErr)
			case admitted && !errors.Is(deleteErr, workflow.ErrProjectBusy):
				t.Fatalf("the deletion was refused for another reason: %v", deleteErr)
			}
		})
	}
}

// A deleted project is kept as it was, read-only: its records, its backup
// sets and its repository stay, it leaves the project list, and Restore
// brings it back active with no deployments.
//
// Kills: a deletion that removes records or backups before the purge; a
// deleted project still listed; a restore that keeps it read-only.
func TestADeletedProjectIsKeptUntilItIsRestored(t *testing.T) {
	t.Parallel()
	a, _, paths := purgeFixture(t)
	before := purgeRemovalCounts(t, a)
	st := deleteProject(t, a, "game")
	if st.State != project.Deleted || !st.KeptUntil.Valid {
		t.Fatalf("deletion: %+v", st)
	}
	if after := purgeRemovalCounts(t, a); after != before {
		t.Fatalf("a kept project's records changed: %+v, want %+v", after, before)
	}
	if kept, err := dataprotection.NewBackups(a.DB).ForDeployment(context.Background(), idstest.N(1), "main", 10); err != nil || len(kept) == 0 {
		t.Fatalf("a kept project's backups: %+v %v", kept, err)
	}
	if _, err := os.Stat(paths[0]); err != nil {
		t.Fatalf("a kept project's repository: %v", err)
	}
	if w := authzCall(a.Handler(), "boss", "GET", "/api/v1/projects", ""); w.Code != 200 || strings.Contains(w.Body.String(), `"slug":"game"`) {
		t.Fatalf("the project list: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(a.Handler(), "boss", "GET", "/api/v1/projects/game", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"deleted"`) {
		t.Fatalf("a kept project's page: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(a.Handler(), "boss", "POST", "/api/v1/projects/game/restore", ""); w.Code != 200 {
		t.Fatalf("restore: %d %s", w.Code, w.Body.String())
	}
	if deps, err := a.Deployment.Deployments.ForProject(t.Context(), idstest.N(1)); err != nil || len(deps) != 0 {
		t.Fatalf("a restored project's deployments: %+v %v", deps, err)
	}
	if w := authzCall(a.Handler(), "boss", "POST", "/api/v1/projects/game/changes", `{"type":"feature","title":"back"}`); w.Code != 201 {
		t.Fatalf("new work in a restored project: %d %s", w.Code, w.Body.String())
	}
}

// A deletion takes a Docker host's deployment down as an operation under
// the deletion's binding: revoked before it runs, it changes nothing, ends
// failed and the deletion stops, saying why.
//
// Kills: a deletion's Docker take-down run inline, outside the ledger and
// its checks.
func TestADeletionsDockerTakeDownIsAnOperationUnderItsBinding(t *testing.T) {
	t.Parallel()
	a, _, _ := purgeFixture(t)
	if w := authzCall(a.Handler(), "boss", "DELETE", "/api/v1/projects/game", `{"confirm":"game"}`); w.Code != 202 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	moveClock(a, projectlifecycle.DeletionGrace+time.Minute)
	ctx := context.Background()
	if _, err := a.deletion.Pass(ctx, "project/1", nil); err != nil {
		t.Fatal(err)
	}
	st, err := a.deletion.Status(ctx, idstest.N(1))
	if err != nil || st.TakeDown == nil || st.TakeDown.Authority.Credential != identity.BindingCredential(st.BindingID) || st.TakeDown.Authority.Person != "boss" {
		t.Fatalf("the take-down: %+v %v", st.TakeDown, err)
	}
	if err := a.DB.InTx(ctx, func(tx *database.Tx) error {
		return identity.RevokeBindingIn(ctx, tx, st.BindingID, "boss", "stop", a.cfg.clock.Now())
	}); err != nil {
		t.Fatal(err)
	}
	op, err := a.operations.RunHere(ctx, st.TakeDown.ID)
	if err != nil || op.State != operations.Failed || !strings.Contains(op.Detail, "authority withdrawn") {
		t.Fatalf("a take-down under a revoked binding: %+v %v", op, err)
	}
	if _, err := a.Deployment.Deployments.ByName(ctx, idstest.N(1), "main"); err != nil {
		t.Fatalf("main went under a revoked binding: %v", err)
	}
	if after := passDeletion(t, a, idstest.N(1)); after.State != project.Deleting || after.Stalled == "" {
		t.Fatalf("after the refused take-down: %+v", after)
	}
}
