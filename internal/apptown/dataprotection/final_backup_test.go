package dataprotection

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// takeDownWorld is a Docker-host deployment that recorded a web part, a
// worker, Postgres with its data volume, the app's data volume and a
// network. The contract at head no longer declares Postgres: the final
// backup follows what the deployment holds, not what the project says now.
func takeDownWorld(t *testing.T, slug string) (*both, *dockerfake.Daemon, *deployment.Deployment) {
	t.Helper()
	return takeDownWorldFor(t, project.Ordinary, slug)
}

// takeDownWorldFor is takeDownWorld for a project made with purpose.
func takeDownWorldFor(t *testing.T, purpose project.Purpose, slug string) (*both, *dockerfake.Daemon, *deployment.Deployment) {
	t.Helper()
	s, _, client, dir, pid := backupWorldFor(t, purpose, slug, 40100)
	s.authority = &configurationWorld{actor: configurationIdentity{Name: "alice"}}
	if err := os.WriteFile(filepath.Join(dir, "workspaces", "project-"+pid.String(), ".apptown", "app.yaml"), []byte("app:\n  web:\n    image: registry.example/"+slug+":1\n    ports: [\"8080\"]\n    public: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target, err := s.Targets.ByName(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	dep, err := s.Deployments.EnsureStanding(t.Context(), pid, "staging", target.ID, deployment.Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	names := hostNamesOf(t, dep, slug)
	for _, o := range []deploy.Owned{
		{Kind: "network", ID: names.Network()},
		{Kind: "container", ID: "c-pg", Name: names.Service("postgres"), Need: "postgres"},
		{Kind: "volume", ID: "v-pg", Data: true, Need: "postgres"},
		{Kind: "container", ID: "c-worker", Name: names.Service("worker")},
		{Kind: "volume", ID: names.DataVolume(), Data: true},
		{Kind: "container", ID: "c-web", Name: names.App()},
	} {
		if o.Kind == "volume" {
			client.SetVolume(o.ID, true)
		}
		if err := s.Deployments.OwnerOf(dep).Created(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
	// staging runs a release that, like head, no longer declares Postgres.
	standRelease(t, s, slug, "staging", "app:\n  web:\n    image: registry.example/"+slug+":1\n    ports: [\"8080\"]\n    public: true\n")
	for _, c := range []string{"c-pg", "c-worker", "c-web"} {
		client.SetRunning(c, true)
	}
	hostReplies(client, nil)
	return s, client, dep
}

// hostReplies answers what the take-down world's host holds that the fake
// daemon does not model (Postgres mounts v-pg where it keeps its data),
// after extra, which a test uses to script a failure.
func hostReplies(client *dockerfake.Daemon, extra func(line string) ([]byte, error, bool)) {
	hostMounts(client, map[string]string{"c-pg": "v-pg\t/var/lib/postgresql/data"}, extra)
}

// setCopies answers the commands whose output is a copy (they stream to a
// file) and leaves every other command to the daemon's own state.
func setCopies(client *dockerfake.Daemon, reply func(docker.Command) ([]byte, error)) {
	client.SetCommandReply(func(req docker.Command) ([]byte, error, bool) {
		if req.Stdout == nil {
			return nil, nil, false
		}
		out, err := reply(req)
		return out, err, true
	})
}

func commandsOf(client *dockerfake.Daemon) []string {
	var out []string
	for _, c := range client.Commands() {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

func indexOf(cmds []string, prefix string) int {
	return slices.IndexFunc(cmds, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

// Every application part stops before the copy, Postgres is dumped in its
// own container although head no longer declares it, and the data is
// deleted only after a complete set, which is kept with its expiry.
func TestTakeDownBacksUpWhatTheDeploymentHoldsThenDeletesIt(t *testing.T) {
	t.Parallel()
	s, client, dep := takeDownWorld(t, "fbk")
	setCopies(client, func(req docker.Command) ([]byte, error) {
		_, err := containerresulttest.WriteString(req.Stdout, "copy")
		return nil, err
	})
	result, err := s.Remove(context.Background(), removeSelection(dep))
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalBackup == nil || result.FinalBackup.Bytes == 0 {
		t.Fatalf("no final set: %+v", result)
	}
	b, err := s.backups.ByID(context.Background(), dep.ProjectID, dep.Name, result.FinalBackup.ID)
	if err != nil || b.Trigger != TriggerTakeDown || b.Status != BackupOK || b.GoodCopy("need", "postgres") == nil || b.GoodCopy("volume", hostNamesOf(t, dep, "fbk").DataVolume()) == nil {
		t.Fatalf("final set: %+v %v", b, err)
	}
	cmds := commandsOf(client)
	dump := indexOf(cmds, "exec -e PGDATABASE -e PGHOST -e PGPASSWORD -e PGPORT -e PGSSLMODE -e PGUSER c-pg sh -c pg_dump")
	for _, c := range []string{"stop c-web", "stop c-worker"} {
		if i := indexOf(cmds, c); i < 0 || i > dump {
			t.Fatalf("%s before the dump (at %d):\n%s", c, dump, strings.Join(cmds, "\n"))
		}
	}
	if indexOf(cmds, "stop c-pg") >= 0 {
		t.Fatal("the need was stopped before its copy")
	}
	// A container goes without its anonymous volumes; only the
	// volumes the list names are deleted, one by one.
	for _, c := range cmds {
		if strings.HasPrefix(c, "rm ") && strings.Contains(" "+c+" ", " -v ") {
			t.Fatalf("a container was removed with its unlisted volumes: %s", c)
		}
	}
	for _, c := range []string{"volume rm v-pg", "volume rm " + hostNamesOf(t, dep, "fbk").DataVolume(), "rm -f c-pg", "network rm " + hostNamesOf(t, dep, "fbk").Network()} {
		if i := indexOf(cmds, c); i < dump {
			t.Fatalf("%s after the copy:\n%s", c, strings.Join(cmds, "\n"))
		}
	}
	rows, err := s.Retained.ForProject(context.Background(), dep.ProjectID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("nothing is retained when everything was deleted: %+v %v", rows, err)
	}
	if _, err := s.Deployments.ByName(t.Context(), dep.ProjectID, dep.Name); err == nil {
		t.Fatal("the deployment outlived its take-down")
	}
}

// A set that falls short, however it falls short, removes nothing: the
// parts are started again and the deployment stands.
func TestATakeDownWhoseFinalBackupFallsShortRemovesNothing(t *testing.T) {
	for _, scenario := range []string{"partial", "failed", "need container gone", "stop refused", "volume unknown", "part unknown", "older volume", "need mounts another"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s, client, dep := takeDownWorld(t, "fbs")
			setCopies(client, func(req docker.Command) ([]byte, error) {
				all := strings.Join(req.Args, " ")
				if scenario == "failed" || (scenario == "partial" && strings.Contains(all, "pg_dump")) {
					return nil, errors.New("copy failed")
				}
				_, err := containerresulttest.WriteString(req.Stdout, "copy")
				return nil, err
			})
			switch scenario {
			case "need container gone":
				client.SetRunning("c-pg", false)
			case "volume unknown", "part unknown":
				// A daemon that cannot be asked is not a daemon that
				// says "gone": the first inspection fails, later ones answer.
				target := map[string]string{"volume unknown": "volume inspect v-pg", "part unknown": "inspect c-worker"}[scenario]
				failed := false
				hostReplies(client, func(line string) ([]byte, error, bool) {
					if line == target && !failed {
						failed = true
						return []byte("error during connect: connection reset by peer"), errors.New("exit 1"), true
					}
					return nil, nil, false
				})
			case "older volume":
				client.SetVolume("old-anonymous-pg", true)
				// An older recorded Postgres volume that no running
				// container mounts is not covered by a dump of the current one.
				if err := s.Deployments.OwnerOf(dep).Created(context.Background(), deploy.Owned{Kind: "volume", ID: "old-anonymous-pg", Data: true, Need: "postgres"}); err != nil {
					t.Fatal(err)
				}
			case "need mounts another":
				hostReplies(client, func(line string) ([]byte, error, bool) {
					if strings.Contains(line, ".Mounts") && strings.HasSuffix(line, " c-pg") {
						return []byte("some-other-volume\t/var/lib/postgresql/data\n"), nil, true
					}
					return nil, nil, false
				})
			case "stop refused":
				hostReplies(client, func(line string) ([]byte, error, bool) {
					if line == "stop c-worker" {
						return []byte("refused"), errors.New("exit 1"), true
					}
					return nil, nil, false
				})
			}
			_, err := s.Remove(context.Background(), removeSelection(dep))
			if !errors.Is(err, ErrFinalBackupIncomplete) {
				t.Fatalf("take-down went on: %v", err)
			}
			cmds := commandsOf(client)
			for _, c := range []string{"volume rm", "network rm", "rm "} {
				if i := indexOf(cmds, c); i >= 0 {
					t.Fatalf("%s after a short backup:\n%s", c, strings.Join(cmds, "\n"))
				}
			}
			// Every part the take-down stopped is started again.
			for _, c := range cmds {
				if id, ok := strings.CutPrefix(c, "stop "); ok && !client.Running(id) && scenario != "stop refused" && scenario != "part unknown" {
					t.Fatalf("%s was stopped and not started again:\n%s", id, strings.Join(cmds, "\n"))
				}
			}
			// A shortfall seen while planning refuses before anything is
			// stopped; one seen while copying starts the parts again.
			switch scenario {
			case "volume unknown", "older volume", "need mounts another", "need container gone":
				if i := indexOf(cmds, "stop "); i >= 0 {
					t.Fatalf("a take-down that could not cover its data stopped a part:\n%s", strings.Join(cmds, "\n"))
				}
			case "partial", "failed":
				if indexOf(cmds, "start c-web") < 0 {
					t.Fatalf("the web part was not started again:\n%s", strings.Join(cmds, "\n"))
				}
			}
			if _, err := s.Deployments.ByName(t.Context(), dep.ProjectID, dep.Name); err != nil {
				t.Fatalf("the deployment went: %v", err)
			}
			if owned, err := s.Deployments.Owned(context.Background(), dep.ID); err != nil || len(owned) < 6 {
				t.Fatalf("its list changed: %+v %v", owned, err)
			}
		})
	}
}

// A final set belongs to its project: the newest-seven rule never pushes it
// out and no age prunes it; it goes, files first and then its record, when
// the project's backups are purged.
func TestFinalBackupsAreKeptUntilTheProjectIsPurged(t *testing.T) {
	t.Parallel()
	s, client, dep := takeDownWorld(t, "fbe")
	setCopies(client, func(req docker.Command) ([]byte, error) {
		_, err := containerresulttest.WriteString(req.Stdout, "copy")
		return nil, err
	})
	result, err := s.Remove(context.Background(), removeSelection(dep))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.backups.ByID(context.Background(), dep.ProjectID, dep.Name, result.FinalBackup.ID)
	// Newer sets under the same name (a deployment made again) push out
	// the old ones past BackupKeep, never the final set.
	root := filepath.Dir(filepath.Dir(filepath.Dir(b.Path)))
	for i := 0; i <= BackupKeep; i++ {
		dir := filepath.Join(root, "fbe", dep.Name, "later-"+string(rune('a'+i)))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := s.backups.Record(context.Background(), &Backup{ProjectID: dep.ProjectID, Deployment: dep.Name, TakenAt: time.Now().UTC().Format(time.RFC3339), Trigger: "scheduled", Path: dir, Status: BackupOK}, "apptown-system"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.backups.Prune(context.Background(), root, dep.ProjectID, dep.Name, BackupKeep); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.backups.ByID(context.Background(), dep.ProjectID, dep.Name, b.ID); got.Status != BackupOK {
		t.Fatalf("count pruning took the final set: %s", got.Status)
	}
	// A set AppTown cannot remove (its files are not under the backup
	// root) is named and keeps its record, for the next purge.
	stray, err := s.backups.Record(context.Background(), &Backup{ProjectID: dep.ProjectID, Deployment: dep.Name, TakenAt: time.Now().UTC().Format(time.RFC3339), Trigger: "scheduled", Path: t.TempDir(), Status: BackupOK}, "apptown-system")
	if err != nil {
		t.Fatal(err)
	}
	n, left, err := s.PurgeProjectBackups(context.Background(), dep.ProjectID, nil)
	if err != nil || len(left) != 1 || !strings.Contains(left[0], fmt.Sprintf("set %d of %s", stray, dep.Name)) || n != 1+BackupKeep {
		t.Fatalf("purge: %d %v %v", n, left, err)
	}
	if _, err := s.backups.ByID(context.Background(), dep.ProjectID, dep.Name, b.ID); err == nil {
		t.Fatal("the purged final set's record stands")
	}
	if _, err := os.Stat(b.Path); !os.IsNotExist(err) {
		t.Fatalf("the purged set's files: %v", err)
	}
	// The ordinary sets went too: nothing newer would ever have pruned them.
	sets, err := s.backups.ForDeployment(context.Background(), dep.ProjectID, dep.Name, 100)
	if err != nil || len(sets) != 1 || sets[0].ID != stray {
		t.Fatalf("the project's sets after its purge: %+v %v", sets, err)
	}
}

// The sets of projects deleted before projects were kept go 30 days after
// each deletion, files first and then their records; a set whose project
// still stands is not touched.
//
// Kills: orphaned sets kept for ever (the leak), or removed before their
// 30 days; a standing project's set removed as an orphan.
func TestOrphanedSetsGoThirtyDaysAfterTheirProjectsDeletion(t *testing.T) {
	t.Parallel()
	s, _, dep := takeDownWorld(t, "orph")
	root := filepath.Join(s.dataDir, "backups")
	type set struct {
		ID, ProjectID ids.ID
		Path          string
	}
	record := func(projectID ids.ID, name string) set {
		t.Helper()
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		id, err := s.backups.Record(context.Background(), &Backup{ProjectID: projectID, Deployment: "main", TakenAt: time.Now().UTC().Format(time.RFC3339), Trigger: "scheduled", Path: dir, Status: BackupOK}, "apptown-system")
		if err != nil {
			t.Fatal(err)
		}
		return set{id, projectID, dir}
	}
	gone := record(idstest.N(999999), "gone")
	standing := record(dep.ProjectID, "standing")
	if n, left, err := s.PurgeOrphanBackups(context.Background(), time.Now().Add(29*24*time.Hour)); err != nil || n != 0 || len(left) != 0 {
		t.Fatalf("before 30 days: %d %v %v", n, left, err)
	}
	if _, err := os.Stat(gone.Path); err != nil {
		t.Fatalf("an orphaned set went before its 30 days: %v", err)
	}
	if n, left, err := s.PurgeOrphanBackups(context.Background(), time.Now().Add(31*24*time.Hour)); err != nil || n != 1 || len(left) != 0 {
		t.Fatalf("after 30 days: %d %v %v", n, left, err)
	}
	if _, err := os.Stat(gone.Path); !os.IsNotExist(err) {
		t.Fatalf("the orphaned set's files: %v", err)
	}
	if _, err := s.backups.ByID(context.Background(), gone.ProjectID, "main", gone.ID); err == nil {
		t.Fatal("the orphaned set's record stands")
	}
	if got, err := s.backups.ByID(context.Background(), dep.ProjectID, "main", standing.ID); err != nil || got.Status != BackupOK {
		t.Fatalf("a standing project's set: %+v %v", got, err)
	}
}

// A taken-down deployment's final set restores into another deployment by
// role: its data volume into the destination's app volume and Postgres
// into the destination's Postgres. A destination with nowhere to put an
// item refuses before anything changes.
func TestAFinalSetRestoresIntoAnotherDeploymentByRole(t *testing.T) {
	for _, scenario := range []string{"declares both", "no postgres", "destination volume copy fails", "destination database copy fails"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s, client, dep := takeDownWorld(t, "frs")
			var restores []string
			setCopies(client, func(req docker.Command) ([]byte, error) {
				_, err := containerresulttest.WriteString(req.Stdout, "copy")
				return nil, err
			})
			result, err := s.Remove(context.Background(), removeSelection(dep))
			if err != nil {
				t.Fatal(err)
			}
			// main runs a release that declares Postgres, or one that never
			// had it (nothing on its list either).
			if scenario != "no postgres" {
				releaseMain(t, s, "frs", "app:\n  web:\n    image: registry.example/frs:1\n    ports: [\"8080\"]\n    public: true\n    dataDir: /data\nneeds:\n  postgres: {}\n")
			} else {
				releaseMain(t, s, "frs", "app:\n  web:\n    image: registry.example/frs:1\n    ports: [\"8080\"]\n    public: true\n    dataDir: /data\n")
			}
			client.ResetCommands()
			client.SetCommandReply(func(req docker.Command) ([]byte, error, bool) {
				switch {
				case req.Stdout != nil:
					// The destination's safety copy of one item fails.
					line := strings.Join(req.Args, " ")
					if (scenario == "destination volume copy fails" && strings.Contains(line, ":/src:ro")) || (scenario == "destination database copy fails" && strings.Contains(line, "pg_dump")) {
						return nil, errors.New("no space left on device"), true
					}
					_, err := containerresulttest.WriteString(req.Stdout, "safety copy")
					return nil, err, true
				case req.Stdin != nil:
					restores = append(restores, strings.Join(req.Args, " "))
					return nil, nil, true
				}
				return nil, nil, false
			})
			res, err := s.restoreBackup(context.Background(), nil, dep.ProjectID, "main", result.FinalBackup.ID, "alice")
			if scenario == "no postgres" {
				if !errors.Is(err, ErrRestoreMismatch) || !strings.Contains(err.Error(), "postgres") {
					t.Fatalf("restore into a deployment without postgres: %v %v", res, err)
				}
				// Reading what main holds asks the host; nothing more.
				for _, c := range commandsOf(client) {
					if !strings.HasPrefix(c, "inspect ") && !strings.HasPrefix(c, "volume inspect ") {
						t.Fatalf("a refused restore touched the host: %v", client.Commands())
					}
				}
				return
			}
			if strings.HasSuffix(scenario, "copy fails") {
				if err == nil || !strings.Contains(err.Error(), "nothing was restored") || len(restores) != 0 {
					t.Fatalf("a restore over an incomplete safety copy: %v %v writes %v", res, err, restores)
				}
				return
			}
			if err != nil || res.Status != RestoreNotVerified || res.From != "staging" {
				t.Fatalf("restore: %v %v", res, err)
			}
			joined := strings.Join(restores, "\n")
			if !strings.Contains(joined, "-v "+deployedNames(t, s, dep.ProjectID, "main", "frs").DataVolume()+":/dst") || !strings.Contains(joined, "exec -i "+pgConnection+" "+deployedNames(t, s, dep.ProjectID, "main", "frs").Service("postgres")+" sh -c pg_restore") {
				t.Fatalf("restored into the wrong places:\n%s", joined)
			}
		})
	}
}

// A copy certifies the recorded volume it was taken from and no
// other: an item of the right kind and name copied from another volume
// leaves the planned volume uncovered.
func TestAFinalCopyCertifiesOnlyItsOwnVolume(t *testing.T) {
	t.Parallel()
	plan := []backupPlanItem{{Kind: "need", Name: "postgres", Volume: "old-anonymous-pg", Listed: true}}
	b := &Backup{Status: BackupOK, Items: []BackupItem{{Kind: "need", Name: "postgres", Volume: "v-pg", File: "postgres.dump"}}}
	if why := b.uncovered(plan); !strings.Contains(why, "old-anonymous-pg") {
		t.Fatalf("a copy of v-pg certified old-anonymous-pg: %q", why)
	}
	b.Items[0].Volume = "old-anonymous-pg"
	if why := b.uncovered(plan); why != "" {
		t.Fatalf("its own copy: %q", why)
	}
}

// A take-down run for a deletion asks its authority again before each
// effect: withdrawn while its final backup is being taken, the backup is
// kept and nothing more happens (the app stands, its records stay and no
// receipt is written); withdrawn after the teardown, its keys and records
// are left and the error says the app went. A take-down that runs through
// names the binding and the person in its receipt.
//
// Kills: a teardown, key revocation or record removal under withdrawn
// authority; a receipt that names only apptown-system.
func TestARemovalAsksAgainBeforeEachEffect(t *testing.T) {
	t.Parallel()
	binding := access.Principal{Kind: "instrument", Name: "apptown-system", Via: "binding", Instrument: "forge", Person: "bls", Credential: "system:binding/7"}
	for _, during := range []string{"before", "final backup", "teardown", "none"} {
		t.Run(during, func(t *testing.T) {
			t.Parallel()
			s, client, dep := takeDownWorld(t, "ask"+strings.ReplaceAll(during, " ", ""))
			var withdrawn atomic.Bool
			setCopies(client, func(req docker.Command) ([]byte, error) {
				if during == "final backup" {
					withdrawn.Store(true)
				}
				_, err := containerresulttest.WriteString(req.Stdout, "copy")
				return nil, err
			})
			if during == "teardown" {
				s.applied = func() { withdrawn.Store(true) }
			}
			withdrawn.Store(during == "before")
			allowed := func(context.Context) error {
				if withdrawn.Load() {
					return operations.ErrAuthorityWithdrawn
				}
				return nil
			}
			_, err := s.remove(context.Background(), removeSelection(dep), binding, allowed, nil)
			stands := func() bool { _, err := s.Deployments.ByName(t.Context(), dep.ProjectID, dep.Name); return err == nil }
			receipts, rerr := audit.Recent(context.Background(), s.db, audit.Filter{ProjectID: dep.ProjectID, KindPrefix: "deployment.torn-down"})
			if rerr != nil {
				t.Fatal(rerr)
			}
			switch during {
			case "before":
				sets, _ := s.backups.ForDeployment(context.Background(), dep.ProjectID, dep.Name, 10)
				if !errors.Is(err, operations.ErrAuthorityWithdrawn) || len(sets) != 0 || !client.Running("c-web") || !stands() {
					t.Fatalf("withdrawn before it began: %v sets=%d", err, len(sets))
				}
			case "final backup":
				if !errors.Is(err, operations.ErrAuthorityWithdrawn) || !strings.Contains(err.Error(), "final backup (set") {
					t.Fatalf("withdrawn during the final backup: %v", err)
				}
				if !client.Running("c-web") || !stands() || len(receipts) != 0 {
					t.Fatalf("an effect after withdrawal: web running=%v record=%v receipts=%d", client.Running("c-web"), stands(), len(receipts))
				}
			case "teardown":
				if !errors.Is(err, operations.ErrAuthorityWithdrawn) || !strings.Contains(err.Error(), "the app was taken down") {
					t.Fatalf("withdrawn during the teardown: %v", err)
				}
				if !stands() || len(receipts) != 0 {
					t.Fatalf("records removed after withdrawal: record=%v receipts=%d", stands(), len(receipts))
				}
			default:
				if err != nil || stands() || len(receipts) != 1 {
					t.Fatalf("the take-down: %v record=%v receipts=%d", err, stands(), len(receipts))
				}
				if p := string(receipts[0].Payload); !strings.Contains(p, `"credential":"system:binding/7"`) || !strings.Contains(p, `"person":"bls"`) {
					t.Fatalf("the receipt's authority: %s", p)
				}
			}
		})
	}
}

// takeDownOps gives a take-down world the operations ledger, as AppTown
// runs a deletion's take-downs.
func takeDownOps(t *testing.T, s *both) (*operations.Service, *grants) {
	t.Helper()
	if err := jobs.Migrate(context.Background(), s.db.Pool()); err != nil {
		t.Fatal(err)
	}
	auth := &grants{who: "alice", deny: map[string]bool{}}
	s.authority = auth
	s.ops = operations.New(s.db, auth, auth, nil)
	s.registerKinds()
	return s.ops, auth
}

// A removal run as a take-down operation honours a stop asked for while its
// final backup is taken: it ends cancelled before the teardown, the backup
// kept and the deployment standing. Its parts are started again, and what
// it says of them is what happened: a part that did not start is named.
//
// Kills: a stop accepted by the ledger and ignored by the removal; a stop
// that says its parts started when one did not.
func TestARemovalOperationStopsAsAskedBeforeItsTeardown(t *testing.T) {
	t.Parallel()
	for _, start := range []string{"starts", "refused"} {
		t.Run(start, func(t *testing.T) {
			t.Parallel()
			s, client, dep := takeDownWorld(t, "stop"+start)
			ops, _ := takeDownOps(t, s)
			ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "alice", Credential: "token:alice"}, Verified: true})
			op, err := s.admit(ctx, dep, OperationTakeDown, access.Principal{Kind: "user", Name: "alice", Credential: "token:alice"}, []string{deployment.SubjectOf(dep)}, map[string]any{"removal": true}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var asked atomic.Bool
			setCopies(client, func(req docker.Command) ([]byte, error) {
				if asked.CompareAndSwap(false, true) {
					if _, err := ops.Cancel(ctx, op.ID); err != nil {
						t.Errorf("cancel: %v", err)
					}
				}
				_, err := containerresulttest.WriteString(req.Stdout, "copy")
				return nil, err
			})
			if start == "refused" {
				hostReplies(client, func(line string) ([]byte, error, bool) {
					if line == "start c-web" {
						return []byte("Error response from daemon: cannot start c-web"), errors.New("exit status 1"), true
					}
					return nil, nil, false
				})
			}
			ended, err := ops.RunHere(context.Background(), op.ID)
			if err != nil {
				t.Fatal(err)
			}
			if ended.State != operations.Cancelled || !strings.Contains(ended.Detail, "stopped as asked before its teardown") {
				t.Fatalf("a stopped removal: %s %q", ended.State, ended.Detail)
			}
			switch start {
			case "starts":
				if !strings.Contains(ended.Detail, "its parts were started again") || !client.Running("c-web") || !client.Running("c-worker") {
					t.Fatalf("the parts after a stop: %q web=%v", ended.Detail, client.Running("c-web"))
				}
			default:
				if strings.Contains(ended.Detail, "were started again") || !strings.Contains(ended.Detail, "did not start again: c-web") {
					t.Fatalf("a part that did not start: %q", ended.Detail)
				}
			}
			if _, err := s.Deployments.ByName(ctx, dep.ProjectID, dep.Name); err != nil {
				t.Fatalf("the deployment went: %v", err)
			}
			sets, err := s.backups.ForDeployment(ctx, dep.ProjectID, dep.Name, 10)
			if err != nil || len(sets) != 1 || sets[0].Trigger != TriggerTakeDown {
				t.Fatalf("the final backup: %+v %v", sets, err)
			}
		})
	}
}

// A restore and a removal of one deployment exclude each other: while a
// restore writes it, a take-down (a deletion's) is refused before it stops,
// copies or deletes anything, and once the restore ends it goes through.
//
// Kills: a final backup copying, or a take-down deleting, a volume a
// restore is still writing.
func TestARestoreAndATakeDownOfOneDeploymentExcludeEachOther(t *testing.T) {
	t.Parallel()
	s, client, dep := takeDownWorld(t, "excl")
	release, err := s.dataWork.claim(dep.ID, "a restore of "+dep.Name)
	if err != nil {
		t.Fatal(err)
	}
	client.ResetCommands()
	if _, err := s.remove(context.Background(), removeSelection(dep), access.Principal{Kind: "user", Name: "alice", Credential: "token:alice"}, nil, nil); err == nil || !strings.Contains(err.Error(), "a restore of staging is under way") {
		t.Fatalf("a take-down during a restore: %v", err)
	}
	if cmds := client.Commands(); len(cmds) != 0 {
		t.Fatalf("the host was reached during the restore: %v", cmds)
	}
	release()
	setCopies(client, func(req docker.Command) ([]byte, error) {
		_, err := containerresulttest.WriteString(req.Stdout, "copy")
		return nil, err
	})
	if _, err := s.remove(context.Background(), removeSelection(dep), access.Principal{Kind: "user", Name: "alice", Credential: "token:alice"}, nil, nil); err != nil {
		t.Fatalf("the take-down once the restore ended: %v", err)
	}
}

// offListWorld is the take-down world whose running release also declares
// Redis, kept in its named volume by a running service AppTown did not
// record (made before it recorded what it creates): held, not listed.
func offListWorld(t *testing.T, slug string) (*both, *dockerfake.Daemon, *deployment.Deployment, string) {
	t.Helper()
	s, client, dep := takeDownWorld(t, slug)
	names := hostNamesOf(t, dep, slug)
	svc, vol := names.Service("redis"), names.NeedVolume("redis")
	standRelease(t, s, slug, "staging", "app:\n  web:\n    image: registry.example/"+slug+":1\n    ports: [\"8080\"]\n    public: true\nneeds:\n  redis: {}\n")
	client.SetRunning(svc, true)
	hostMounts(client, map[string]string{"c-pg": "v-pg\t/var/lib/postgresql/data", svc: vol + "\t/data"}, nil)
	return s, client, dep, vol
}

// A volume the deployment holds but its list lacks is copied into the final
// set and kept: nothing deletes it, and it is retained saying whether the
// set holds a copy. Its failed copy does not refuse the take-down, since
// nothing deletes it; the listed data still goes only with good copies.
//
// Kills: leaving off-list data out of the final set; deleting it; losing
// it silently (no retained row); refusing a take-down for data it keeps.
func TestAVolumeOffTheListIsCopiedKeptAndRetained(t *testing.T) {
	for _, copies := range []bool{true, false} {
		t.Run(fmt.Sprint("copied=", copies), func(t *testing.T) {
			t.Parallel()
			slug := map[bool]string{true: "ofl", false: "ofn"}[copies]
			s, client, dep, vol := offListWorld(t, slug)
			setCopies(client, func(req docker.Command) ([]byte, error) {
				if !copies && strings.Contains(strings.Join(req.Args, " "), "redis") {
					return nil, errors.New("copy failed")
				}
				_, err := containerresulttest.WriteString(req.Stdout, "copy")
				return nil, err
			})
			result, err := s.Remove(context.Background(), removeSelection(dep))
			if err != nil || result.FinalBackup == nil {
				t.Fatalf("take-down: %+v %v", result, err)
			}
			b, err := s.backups.ByID(context.Background(), dep.ProjectID, dep.Name, result.FinalBackup.ID)
			if err != nil {
				t.Fatal(err)
			}
			var redis *BackupItem
			for i := range b.Items {
				if b.Items[i].Volume == vol {
					redis = &b.Items[i]
				}
			}
			if redis == nil || (redis.Error == "") != copies {
				t.Fatalf("the off-list volume in the final set: %+v", b.Items)
			}
			cmds := commandsOf(client)
			if indexOf(cmds, "volume rm "+vol) >= 0 {
				t.Fatalf("the off-list volume was deleted:\n%s", strings.Join(cmds, "\n"))
			}
			if indexOf(cmds, "volume rm v-pg") < 0 {
				t.Fatalf("the listed volume was not deleted:\n%s", strings.Join(cmds, "\n"))
			}
			rows, err := s.Retained.ForProject(context.Background(), dep.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			want := map[bool]string{true: "holds a copy", false: "holds no copy"}[copies]
			for _, r := range rows {
				if r.Need == "volume "+vol {
					if why, _ := r.Handle["why"].(string); !strings.Contains(why, "not on the deployment's list") || !strings.Contains(why, want) {
						t.Fatalf("retained with why %q", why)
					}
					return
				}
			}
			t.Fatalf("the off-list volume is not retained: %+v", rows)
		})
	}
}

// A need container on the list keeping data in its own files would lose it
// with the container: the take-down refuses before stopping anything,
// whatever its release says of the need (declared, dropped, unread) and
// whatever other volume of that name is off the list.
//
// Kills: deciding from the release or the plan rather than from each
// listed need container; deleting a container holding uncopied data.
func TestATakeDownRefusesToDeleteAContainerHoldingUncopiedData(t *testing.T) {
	withRedis := "app:\n  web:\n    image: registry.example/%s:1\n    ports: [\"8080\"]\n    public: true\nneeds:\n  redis: {}\n"
	without := "app:\n  web:\n    image: registry.example/%s:1\n    ports: [\"8080\"]\n    public: true\n"
	for _, tc := range []struct {
		name, release string
		namedVolume   bool   // an off-list volume of the need's expected name is there
		need, says    string // the listed container's need, and the refusal's words
	}{
		{"declared", withRedis, false, "redis", "its own files"},
		{"dropped", without, false, "redis", "its own files"},
		{"release unread", "app: [not a contract", false, "redis", "its own files"},
		{"off-list volume it does not mount", withRedis, true, "redis", "its own files"},
		{"kind unread", without, false, "quantum", "where it keeps data is not known"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			slug := "ctr" + strings.ReplaceAll(tc.name, " ", "")[:6]
			s, client, dep, vol := offListWorld(t, slug)
			release := tc.release
			if strings.Contains(release, "%s") {
				release = fmt.Sprintf(release, slug)
			}
			standRelease(t, s, slug, "staging", release)
			svc := hostNamesOf(t, dep, slug).Service(tc.need)
			client.SetRunning(svc, true)
			if err := s.Deployments.OwnerOf(dep).Created(context.Background(), deploy.Owned{Kind: "container", ID: svc, Need: tc.need}); err != nil {
				t.Fatal(err)
			}
			hostMounts(client, map[string]string{"c-pg": "v-pg\t/var/lib/postgresql/data"}, func(line string) ([]byte, error, bool) {
				if line == "volume inspect "+vol && !tc.namedVolume {
					return []byte("Error: No such volume"), errors.New("exit 1"), true
				}
				return nil, nil, false
			})
			before, err := s.Deployments.Owned(context.Background(), dep.ID)
			if err != nil {
				t.Fatal(err)
			}
			client.ResetCommands()
			if _, err := s.Remove(context.Background(), removeSelection(dep)); !errors.Is(err, ErrFinalBackupIncomplete) || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("take-down: %v", err)
			}
			for _, c := range commandsOf(client) {
				if strings.HasPrefix(c, "stop ") || strings.HasPrefix(c, "rm ") || strings.HasPrefix(c, "volume rm ") {
					t.Fatalf("changed before refusing: %s", c)
				}
			}
			if after, err := s.Deployments.Owned(context.Background(), dep.ID); err != nil || len(after) != len(before) {
				t.Fatalf("its list changed: %d -> %d %v", len(before), len(after), err)
			}
			if _, err := s.Deployments.ByName(t.Context(), dep.ProjectID, dep.Name); err != nil {
				t.Fatalf("the deployment went: %v", err)
			}
		})
	}
}

// What a take-down keeps off the list is retained as what the host
// answered for: a need container whose own files hold its data is a
// retained container, never a volume with no name; a need whose kind
// cannot be read is retained by its volume only when that volume is there,
// and not at all when neither it nor its container is.
//
// Kills: recording an unresolved item as a retained volume.
func TestOffListDataIsRetainedAsWhatTheHostHolds(t *testing.T) {
	t.Run("container data", func(t *testing.T) {
		t.Parallel()
		s, client, dep, vol := offListWorld(t, "rtc")
		svc := hostNamesOf(t, dep, "rtc").Service("redis")
		hostMounts(client, map[string]string{"c-pg": "v-pg\t/var/lib/postgresql/data"}, func(line string) ([]byte, error, bool) {
			if line == "volume inspect "+vol {
				return []byte("Error: No such volume"), errors.New("exit 1"), true
			}
			return nil, nil, false
		})
		setCopies(client, func(req docker.Command) ([]byte, error) {
			_, err := containerresulttest.WriteString(req.Stdout, "copy")
			return nil, err
		})
		if _, err := s.Remove(context.Background(), removeSelection(dep)); err != nil {
			t.Fatal(err)
		}
		rows := retainedOf(t, s, dep)
		if why := rows["container "+svc]; !strings.Contains(why, "mounts no volume") || rows["volume "] != "" {
			t.Fatalf("retained: %v", rows)
		}
	})
	for _, there := range []bool{true, false} {
		t.Run(fmt.Sprint("unread kind, volume there=", there), func(t *testing.T) {
			t.Parallel()
			slug := map[bool]string{true: "rtq", false: "rtn"}[there]
			s, client, dep := takeDownWorld(t, slug)
			standRelease(t, s, slug, "staging", "app:\n  web:\n    image: x:1\n    public: true\nneeds:\n  quantum: {}\n")
			vol := hostNamesOf(t, dep, slug).NeedVolume("quantum")
			client.SetVolume(vol, there)
			hostReplies(client, func(line string) ([]byte, error, bool) {
				if line == "volume inspect "+vol && !there {
					return []byte("Error: No such volume"), errors.New("exit 1"), true
				}
				return nil, nil, false
			})
			setCopies(client, func(req docker.Command) ([]byte, error) {
				_, err := containerresulttest.WriteString(req.Stdout, "copy")
				return nil, err
			})
			if _, err := s.Remove(context.Background(), removeSelection(dep)); err != nil {
				t.Fatal(err)
			}
			rows := retainedOf(t, s, dep)
			why, retained := rows["volume "+vol]
			if retained != there || (there && !strings.Contains(why, "not in the dependency catalog")) {
				t.Fatalf("retained: %v", rows)
			}
		})
	}
}

// retainedOf is a project's retained rows: what each names, and why.
func retainedOf(t *testing.T, s *both, dep *deployment.Deployment) map[string]string {
	t.Helper()
	rows, err := s.Retained.ForProject(context.Background(), dep.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range rows {
		why, _ := r.Handle["why"].(string)
		out[r.Need] = why
	}
	return out
}
