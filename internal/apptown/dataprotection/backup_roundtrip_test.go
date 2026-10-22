package dataprotection

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/stock"
)

func backupWorld(t *testing.T, slug string, port int) (*both, *database.DB, *dockerfake.Daemon, string, ids.ID) {
	t.Helper()
	return backupWorldFor(t, project.Ordinary, slug, port)
}

// backupWorldFor is backupWorld for a project made with purpose.
func backupWorldFor(t *testing.T, purpose project.Purpose, slug string, port int) (*both, *database.DB, *dockerfake.Daemon, string, ids.ID) {
	t.Helper()
	db := testDB(t)
	pid := createProjectFor(t, db, slug, strings.ToUpper(slug), purpose)
	dir := t.TempDir()
	catalog := dependency.NewCatalog(db, nil)
	if _, _, err := catalog.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	client := dockerfake.New()
	// Restores run through the operations ledger, as bls, whose grants
	// the test may change.
	who := &grants{who: "bls", deny: map[string]bool{}}
	ops := operations.New(db, who, who, nil)
	s := assembled(db, testRuntime{Scope: docker.Scope{Client: client}, Probe: &docker.Probe{Settle: time.Millisecond}, DataDir: dir, Needs: catalog, Secrets: secret.NewService(db, secret.Config{DataDir: dir}), ChownImage: "test-helper", BuiltInTarget: "local"}, deployment.Dependencies{Operations: ops, Authority: who, Releases: deployment.ReleaseDependencies{Sources: &releasedContracts{}}})
	target, err := s.Targets.Put(context.Background(), &deployment.Target{Name: "local", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deployments.EnsureStanding(t.Context(), pid, "main", target.ID, deployment.Attributes{}.WithDefaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deployments.ReserveHostPort(t.Context(), pid, "main", docker.Current.Container(slug, "main"), port); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "workspaces", "project-"+slug, ".apptown")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "app:\n  web:\n    image: registry.example/" + slug + ":1\n    ports: [\"8080\"]\n    public: true\n    dataDir: /data\nneeds:\n  postgres: {}\n"
	if err := os.WriteFile(filepath.Join(repo, "app.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	// main runs that contract: its Postgres runs and mounts its volume.
	releaseMain(t, s, slug, yaml)
	return s, db, client, dir, pid
}

// answeringPort is a port on which an app answers: a restore that starts
// the app again asks it whether it is ready, and it is.
func answeringPort(t *testing.T) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func setBackupDockerCommand(client *dockerfake.Daemon, reply func(docker.Command) ([]byte, error)) {
	client.SetCommandReply(func(req docker.Command) ([]byte, error, bool) {
		// How a container was started, whether it runs, and whether a
		// container or volume is there, are the daemon's to answer.
		if len(req.Args) > 2 && req.Args[0] == "inspect" && req.Args[2] == "{{json .Config}}" ||
			len(req.Args) == 4 && req.Args[0] == "inspect" && req.Args[2] == "{{.State.Running}}" ||
			len(req.Args) == 2 && req.Args[0] == "inspect" || len(req.Args) == 3 && req.Args[0] == "volume" && req.Args[1] == "inspect" {
			return nil, nil, false
		}
		out, err := reply(req)
		return out, err, true
	})
}

func TestBackupPlanAndTake(t *testing.T) {
	t.Parallel()
	s, db, client, dir, pid := backupWorld(t, "bk", 40000)

	// Take: each item's docker command streams to its file.
	var ran []string

	setBackupDockerCommand(client, func(req docker.Command) ([]byte, error) {
		ran = append(ran, strings.Join(req.Args, " "))
		_, err := io.WriteString(req.Stdout, "bytes for "+filepath.Base(req.Stdout.(*os.File).Name()))
		return nil, err
	})
	b, err := s.TakeBackup(context.Background(), pid, "main", "manual", "bls")
	if err != nil || b.Status != BackupOK || len(b.Items) != 2 || b.Bytes == 0 || b.Items[1].SHA256 == "" {
		t.Fatalf("take: %+v %v", b, err)
	}
	if !strings.HasPrefix(b.Path, filepath.Join(dir, "backups", "bk", "main")) {
		t.Fatalf("sets live under the data directory: %s", b.Path)
	}
	raw, err := os.ReadFile(filepath.Join(b.Path, "manifest.json"))
	if err != nil {
		t.Fatal("the set carries its manifest")
	}
	// The manifest says what it is and where the set lives relative to the
	// backups root, never a path on this machine.
	var m struct {
		Format  string
		Version int
		Path    string
	}
	if err := json.Unmarshal(raw, &m); err != nil || m.Format != BackupManifestFormat || m.Version != 1 || m.Path != "bk/main/"+filepath.Base(b.Path) {
		t.Fatalf("manifest: %+v %v\n%s", m, err, raw)
	}
	joined := strings.Join(ran, "\n")
	if !strings.Contains(joined, "-v apptown-data-apptown-env-bk-main:/src:ro") || !strings.Contains(joined, "exec -e PGDATABASE -e PGHOST -e PGPASSWORD -e PGPORT -e PGSSLMODE -e PGUSER apptown-svc-env-bk-main-postgres sh -c pg_dump -Fc --no-owner") {
		t.Fatalf("the volume is tarred read-only and the database exported by its kind's portable copy in its container:\n%s", joined)
	}
	// A failing item leaves a partial set on the record, with the words.
	setBackupDockerCommand(client, func(req docker.Command) ([]byte, error) {
		if strings.Contains(strings.Join(req.Args, " "), "pg_dump") {
			return nil, os.ErrNotExist
		}
		_, err := io.WriteString(req.Stdout, "x")
		return nil, err
	})
	if b, err := s.TakeBackup(context.Background(), pid, "main", "scheduled", "apptown-system"); err != nil || b.Status != BackupPartial || b.Items[1].Error == "" {
		t.Fatalf("partial: %+v %v", b, err)
	}
	// Retention: the newest seven stay.
	setBackupDockerCommand(client, func(req docker.Command) ([]byte, error) {
		_, err := io.WriteString(req.Stdout, "x")
		return nil, err
	})
	for i := 0; i < 8; i++ {
		if _, err := s.TakeBackup(context.Background(), pid, "main", "scheduled", "apptown-system"); err != nil {
			t.Fatal(err)
		}
	}
	bs, _ := s.backups.ForDeployment(context.Background(), pid, "main", 100)
	kept, pruned := 0, 0
	for _, x := range bs {
		switch x.Status {
		case BackupOK, BackupPartial:
			kept++
			if _, err := os.Stat(x.Path); err != nil {
				t.Fatalf("a kept set is on disk: %s", x.Path)
			}
		case BackupPruned:
			pruned++
			if _, err := os.Stat(x.Path); err == nil {
				t.Fatalf("a pruned set is gone from disk: %s", x.Path)
			}
		}
	}
	if kept != BackupKeep || pruned != 3 {
		t.Fatalf("keep the newest %d: kept %d pruned %d", BackupKeep, kept, pruned)
	}

	// A Portainer deployment is backed up through Portainer: one whose
	// environment cannot be reached is not known to hold nothing, and its
	// scheduled backup fails rather than being skipped.
	lan, _ := s.Targets.ByName(t.Context(), "lanx")
	if lan == nil {
		lan, _ = s.Targets.Put(context.Background(), &deployment.Target{Name: "lanx", Kind: deployment.KindPortainer, Origin: deployment.OriginAttached, NeedsMode: "emulators", SecretsDelivery: "inject", Status: deployment.TargetReady,
			Address: map[string]any{"urlFrom": "NO_SUCH_PORTAINER_URL", "apiKeyFrom": "NO_SUCH_PORTAINER_KEY", "environment": "lan"}})
	}
	if _, err := s.Deployments.EnsureStanding(t.Context(), pid, "lan", lan.ID, deployment.Attributes{}.WithDefaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TakeBackup(context.Background(), pid, "lan", "manual", "bls"); !errors.Is(err, ErrDataNotKnown) || errors.Is(err, deployment.ErrTargetKeepsBackups) {
		t.Fatalf("a Portainer deployment whose environment cannot be reached: %v", err)
	}
	// The scheduled job says so, and it skips a project gone since the job
	// was queued; a failed read is retried.
	worker := JobWorker{Service: s.Service, Projects: project.New(db, nil)}
	if receipt, err := worker.Backup(context.Background(), "bk/lan", nil); err == nil || receipt["skipped"] != nil {
		t.Fatalf("a scheduled backup of an unreachable Portainer deployment: %v %v", receipt, err)
	}
	if _, err := worker.Backup(context.Background(), "gone/main", nil); !jobs.IsPermanent(err) {
		t.Fatalf("a project that is gone ends the job: %v", err)
	}
	dbtest.FailNextStatement(t, db, "-- name: ProjectBySlug")
	if _, err := worker.Backup(context.Background(), "bk/lan", nil); err == nil || jobs.IsPermanent(err) {
		t.Fatalf("a failed project read is retried, not permanent: %v", err)
	}
}

// The refusal for a target that keeps its own backups is not skipped when
// the target cannot be read: the backup fails, and no set is recorded.
func TestABackupWhoseTargetCannotBeReadIsRefused(t *testing.T) {
	t.Parallel()
	s, db, _, _, pid := backupWorld(t, "tr", 40002)
	lan, err := s.Targets.Put(context.Background(), &deployment.Target{Name: "lanx", Kind: deployment.KindPortainer, Origin: deployment.OriginAttached, NeedsMode: "emulators", SecretsDelivery: "inject", Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deployments.EnsureStanding(t.Context(), pid, "lan", lan.ID, deployment.Attributes{}.WithDefaults()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.FailNextStatement("-- name: TargetByID "))
	if _, err := s.TakeBackup(context.Background(), pid, "lan", "manual", "bls"); err == nil || !strings.Contains(err.Error(), "reading its deployment target") {
		t.Fatalf("a backup whose target read failed: %v; want the read's failure", err)
	}
	if sets, err := s.backups.ForDeployment(context.Background(), pid, "lan", 10); err != nil || len(sets) != 0 {
		t.Fatalf("sets recorded: %+v %v", sets, err)
	}
}

// A set whose manifest cannot be written beside its copies says so on its
// record; the copies are still the set's.
func TestASetWithoutItsManifestSaysSo(t *testing.T) {
	t.Parallel()
	s, _, client, _, pid := backupWorld(t, "mf", 40003)
	setBackupDockerCommand(client, func(req docker.Command) ([]byte, error) {
		// Fixture: a folder where the manifest goes, so it cannot be written.
		if err := os.MkdirAll(filepath.Join(filepath.Dir(req.Stdout.(*os.File).Name()), "manifest.json"), 0o700); err != nil {
			return nil, err
		}
		_, err := io.WriteString(req.Stdout, "x")
		return nil, err
	})
	b, err := s.TakeBackup(context.Background(), pid, "main", "manual", "bls")
	if err != nil || !strings.Contains(b.Error, "the manifest could not be written") {
		t.Fatalf("take: %+v %v", b, err)
	}
	sets, err := s.backups.ForDeployment(context.Background(), pid, "main", 10)
	if err != nil || len(sets) != 1 || !strings.Contains(sets[0].Error, "the manifest could not be written") {
		t.Fatalf("recorded sets: %+v %v", sets, err)
	}
}

func TestRestoreBackup(t *testing.T) {
	t.Parallel()
	s, db, client, dir, pid := backupWorld(t, "rs", answeringPort(t))
	var lifecycle, restores []string
	// AppTown's clock, which the restore moves an hour on while it loads
	// an item: the outcome's receipt is stamped when it is established.
	began := time.Now().Add(-72 * time.Hour).Truncate(time.Second).UTC()
	var at atomic.Int64
	at.Store(began.Unix())
	s.setClock(func() time.Time { return time.Unix(at.Load(), 0) })

	setBackupDockerCommand(client, func(req docker.Command) ([]byte, error) {
		if req.Stdout != nil {
			_, err := io.WriteString(req.Stdout, "copy of "+filepath.Base(req.Stdout.(*os.File).Name()))
			return nil, err
		}
		if req.Stdin != nil {
			if _, err := s.backups.Prune(context.Background(), filepath.Join(dir, "backups"), pid, "main", BackupKeep); err == nil || !strings.Contains(err.Error(), "busy") {
				t.Fatalf("pruning was not refused during restore: %v", err)
			}

			path := req.Stdin.(*os.File).Name()
			raw, err := io.ReadAll(req.Stdin)
			if err != nil {
				return nil, err
			}
			if string(raw) != "copy of "+filepath.Base(path) {
				t.Fatalf("wrong restore bytes: %q", raw)
			}
			restores = append(restores, filepath.Base(path)+" <- "+strings.Join(req.Args, " "))
			at.Store(began.Add(time.Hour).Unix())
			return nil, nil
		}
		lifecycle = append(lifecycle, strings.Join(req.Args, " "))
		return nil, nil
	})
	b, err := s.TakeBackup(context.Background(), pid, "main", "manual", "bls")
	if err != nil {
		t.Fatal(err)
	}
	// Restore the oldest retained set. Its safety copy would otherwise push
	// this very source past the retention limit before the first file is read.
	for i := 1; i < BackupKeep; i++ {
		if _, err := s.TakeBackup(context.Background(), pid, "main", "scheduled", "apptown-system"); err != nil {
			t.Fatal(err)
		}
	}
	client.SetRunning(docker.Current.Container("rs", "main"), true) // a standing deployment's app runs
	op, res := restoreThroughOperation(t, s, pid, "main", b.ID)
	if op.State != operations.Succeeded {
		t.Fatalf("restore: %s %s", op.State, op.Detail)
	}
	if res.Status != RestoreNotVerified || res.SafetyBackup.Compare(b.ID) <= 0 {
		t.Fatalf("a safety set is taken first and the outcome recorded: %v", res)
	}
	// Postgres is put back in place and checked by its kind's copy.verify,
	// the check a managed database's fill passes; the app's volume is
	// files nothing checked.
	if len(res.Items) != 2 || res.Items[0].Kind != "volume" || res.Items[0].Outcome != RestoreCopied || res.Items[1].Name != "postgres" || res.Items[1].Outcome != RestoreVerified {
		t.Fatalf("what witnessed each item: %+v", res.Items)
	}
	if _, err := os.Stat(b.Path); err != nil {
		t.Fatalf("restore pruned its source: %v", err)
	}
	if lines, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: "restored", Limit: 1}); err != nil || len(lines) != 1 || lines[0].At != began.Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("the outcome's receipt %+v %v, want it stamped when the restore ended, %v", lines, err, began.Add(time.Hour))
	}
	wantN(t, "kept backups", backupsWhere(t, db, func(b ProjectBackup) bool { return b.ProjectID == pid && b.Status == BackupOK }), BackupKeep+1)
	lc := strings.Join(lifecycle, "\n")
	if !strings.Contains(lc, "stop apptown-env-rs-main") || !strings.Contains(lc, "start apptown-env-rs-main") {
		t.Fatalf("the app is stopped and started: %s", lc)
	}
	rj := strings.Join(restores, "\n")
	if !strings.Contains(rj, "apptown-data-apptown-env-rs-main.tar.gz <- run --rm -i --entrypoint  --network none -v apptown-data-apptown-env-rs-main:/dst") || !strings.Contains(rj, "find /dst -mindepth 1 -delete && tar xz -C /dst") {
		t.Fatalf("the volume is emptied and untarred through a helper:\n%s", rj)
	}
	// Both run in the database's container with the connection it serves,
	// which the commands read as PG* variables.
	if !strings.Contains(rj, "apptown-need-env-rs-main-postgres.dump <- exec -i "+pgConnection+" apptown-svc-env-rs-main-postgres sh -c pg_restore -d \"$PGDATABASE\" --clean") ||
		!strings.Contains(rj, "apptown-need-env-rs-main-postgres.dump <- exec -i "+pgConnection+" apptown-svc-env-rs-main-postgres sh -c set -e\npg_restore -l") {
		t.Fatalf("the database is restored by its declared copy in its container, then checked there:\n%s", rj)
	}
	// Tampered file: refused by digest.
	_ = os.WriteFile(filepath.Join(b.Path, "apptown-need-env-rs-main-postgres.dump"), []byte("changed"), 0o600)
	res, err = s.restoreBackup(context.Background(), nil, pid, "main", b.ID, "bls")
	if err == nil || res.Status != RestorePartial {
		t.Fatalf("a file that does not match its digest is not restored: %v %v", err, res)
	}
	if _, err := s.TakeBackup(context.Background(), pid, "main", "scheduled", "apptown-system"); err != nil {
		t.Fatal(err)
	}
	wantN(t, "held backups", backupsWhere(t, db, func(b ProjectBackup) bool { return b.ProjectID == pid && b.Status.Holds() }), BackupKeep)
	wantN(t, "the restored set, pruned", backupsWhere(t, db, func(p ProjectBackup) bool { return p.ID == b.ID && p.Status == BackupPruned }), 1)

}

// A restore overwrites nothing unless its safety set copied everything
// that is there now: when the app volume's copy fails and the database's
// succeeds, or the other way round, the restore is refused before the app
// is stopped or anything is written.
func TestARestoreWhoseSafetySetIsIncompleteWritesNothing(t *testing.T) {
	for _, failing := range []string{"volume", "database"} {
		t.Run(failing, func(t *testing.T) {
			t.Parallel()
			s, _, client, _, pid := backupWorld(t, "rsafe", 40002)
			copies := 0
			var writes, lifecycle []string
			setBackupDockerCommand(client, func(req docker.Command) ([]byte, error) {
				line := strings.Join(req.Args, " ")
				switch {
				case req.Stdout != nil:
					copies++
					// The first set is good; the safety set's chosen copy fails.
					if copies > 2 && ((failing == "volume" && strings.Contains(line, ":/src:ro")) || (failing == "database" && strings.Contains(line, "pg_dump"))) {
						return nil, errors.New("no space left on device")
					}
					_, err := io.WriteString(req.Stdout, "copy")
					return nil, err
				case req.Stdin != nil:
					writes = append(writes, line)
					return nil, nil
				}
				lifecycle = append(lifecycle, line)
				return nil, nil
			})
			b, err := s.TakeBackup(context.Background(), pid, "main", "manual", "bls")
			if err != nil || b.Status != BackupOK {
				t.Fatalf("the set to restore: %+v %v", b, err)
			}
			res, err := s.restoreBackup(context.Background(), nil, pid, "main", b.ID, "bls")
			if err == nil || !strings.Contains(err.Error(), "nothing was restored") {
				t.Fatalf("a restore over an incomplete safety set: %v %v", res, err)
			}
			if len(writes) != 0 {
				t.Fatalf("data was overwritten without its safety copy: %v", writes)
			}
			for _, l := range lifecycle {
				if strings.HasPrefix(l, "stop ") {
					t.Fatalf("the app was stopped for a refused restore: %v", lifecycle)
				}
			}
		})
	}
}
