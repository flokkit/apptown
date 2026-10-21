package dataprotection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockerfake"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/ids"
)

// hostLog answers every copy with the bytes "copy of <file>" (so each
// item's bytes are its own) and records each restore as
// "<file> <- <command>", reading what it was handed.
type hostLog struct {
	mu       sync.Mutex
	restores []string
}

func (h *hostLog) answer(client *dockerfake.Daemon) {
	client.SetCommandReply(func(req docker.Command) ([]byte, error, bool) {
		switch {
		case req.Stdout != nil:
			_, err := io.WriteString(req.Stdout, "copy of "+filepath.Base(req.Stdout.(*os.File).Name()))
			return nil, err, true
		case req.Stdin != nil:
			raw, err := io.ReadAll(req.Stdin)
			h.mu.Lock()
			h.restores = append(h.restores, string(raw)+" <- "+strings.Join(req.Args, " "))
			h.mu.Unlock()
			return nil, err, true
		}
		return nil, nil, false
	})
}

// pgConnection is the docker -e flags the stock Postgres copy's commands
// are given in its container: its copy.env, by name only.
const pgConnection = "-e PGDATABASE -e PGHOST -e PGPASSWORD -e PGPORT -e PGSSLMODE -e PGUSER"

func (h *hostLog) restored() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.restores, "\n")
}

func record(t *testing.T, s *both, dep *deployment.Deployment, owned ...deploy.Owned) {
	t.Helper()
	for _, o := range owned {
		if err := s.Deployments.OwnerOf(dep).Created(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
}

func mainOf(t *testing.T, s *both, pid ids.ID) *deployment.Deployment {
	t.Helper()
	dep, err := s.Deployments.ByName(t.Context(), pid, "main")
	if err != nil {
		t.Fatal(err)
	}
	return dep
}

// The running release's data is planned by the kind's own copy, each item
// bound to its volume and its file named for it.
//
// Kills: planning from the list alone (nothing listed here); a need's file
// named for its role rather than its volume.
func TestADeploymentHoldsWhatItsReleaseDeclares(t *testing.T) {
	t.Parallel()
	s, _, _, _, pid := backupWorld(t, "bk", 40000)
	plan, err := s.heldPlan(t.Context(), pid, "bk", "main")
	if err != nil || len(plan) != 2 {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	app, pg := plan[0], plan[1]
	if app.Kind != "volume" || app.Volume != "apptown-data-apptown-env-bk-main" || app.File != "apptown-data-apptown-env-bk-main.tar.gz" {
		t.Fatalf("the app's volume: %+v", app)
	}
	if pg.Kind != "need" || pg.Name != "postgres" || pg.Volume != "apptown-need-env-bk-main-postgres" || pg.File != "apptown-need-env-bk-main-postgres.dump" ||
		!strings.Contains(pg.How, "pg_dump") || !strings.Contains(pg.Restore, "pg_restore") {
		t.Fatalf("Postgres, dumped by its kind's copy: %+v", pg)
	}
}

// A standing deployment still running a need that head (and its own
// release) no longer declares holds that need's data: an ordinary backup
// copies it where it runs, without stopping anything, and a restore puts it
// back there.
//
// Kills: planning from head or the release alone.
func TestAnOrdinaryBackupCopiesANeedTheHeadNoLongerDeclares(t *testing.T) {
	t.Parallel()
	s, client, dep := takeDownWorld(t, "obk")
	var h hostLog
	h.answer(client)
	b, err := s.TakeBackup(context.Background(), dep.ProjectID, dep.Name, "manual", "bls")
	if err != nil || b.Status != BackupOK {
		t.Fatalf("take: %+v %v", b, err)
	}
	var pg *BackupItem
	for i := range b.Items {
		if b.Items[i].Name == "postgres" {
			pg = &b.Items[i]
		}
	}
	if pg == nil || pg.Volume != "v-pg" || pg.Error != "" || !strings.Contains(pg.How, "in c-pg") {
		t.Fatalf("the dropped need's data is in the set: %+v", b.Items)
	}
	if indexOf(commandsOf(client), "stop ") >= 0 {
		t.Fatalf("an ordinary backup stopped a part:\n%s", strings.Join(commandsOf(client), "\n"))
	}
	if _, err := s.restoreBackup(context.Background(), nil, dep.ProjectID, dep.Name, b.ID, "bls"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.restored(), "copy of v-pg.dump <- exec -i "+pgConnection+" c-pg sh -c pg_restore") {
		t.Fatalf("Postgres goes back where it runs:\n%s", h.restored())
	}
}

// A held volume nothing can copy (no running container mounts it) is in
// the set as not copied, so the set is partial, never ok; the protection
// view shows it with no copy and why.
//
// Kills: leaving such a volume out of the plan; counting the set ok.
func TestAHeldVolumeNothingCanCopyMakesTheSetPartial(t *testing.T) {
	t.Parallel()
	s, client, dep := takeDownWorld(t, "unc")
	hostMounts(client, nil, nil) // Postgres no longer mounts v-pg
	var h hostLog
	h.answer(client)
	b, err := s.TakeBackup(context.Background(), dep.ProjectID, dep.Name, "manual", "bls")
	if err != nil || b.Status != BackupPartial {
		t.Fatalf("take: %+v %v", b, err)
	}
	p, err := s.DeploymentProtection(context.Background(), dep.ProjectID, dep.Name)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range p.Items {
		if row.Volume == "v-pg" {
			if row.LastCopy != nil || row.Copy != CopyNone || !strings.Contains(row.CopyNote, "no running postgres container mounts it") || !strings.Contains(row.CopyNote, "look before deleting it") {
				t.Fatalf("the uncopied volume's row: %+v", row)
			}
			// It is named as not assessed, never counted as covered.
			if slices.Contains(p.Coverage.Assessed, "postgres · volume v-pg") || !slices.Contains(p.Coverage.NotAssessed, "postgres · volume v-pg: "+row.CopyNote) {
				t.Fatalf("the uncopied volume's coverage: %+v", p.Coverage)
			}
			return
		}
	}
	t.Fatalf("no row for the uncopied volume: %+v", p.Items)
}

// "Nothing to back up" is said only when the list and the release were
// both read and neither names data. A deployment with no recorded release,
// or whose release cannot be read, is not known: its backup fails, the
// scheduled one too, and protection reads unknown. Data named but absent
// from the host is not "nothing" either.
//
// Kills: treating an unread source as empty; the empty-plan shortcut.
func TestNothingToBackUpNeedsBothSourcesReadAndEmpty(t *testing.T) {
	t.Parallel()
	s, db, client, _, pid := backupWorld(t, "nb", 40020)
	target, err := s.Targets.ByName(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deployments.EnsureStanding(t.Context(), pid, "fresh", target.ID, deployment.Attributes{}.WithDefaults()); err != nil {
		t.Fatal(err)
	}
	worker := JobWorker{Service: s.Service, Projects: project.New(db, nil)}
	unknown := func(name string) {
		t.Helper()
		if _, err := s.TakeBackup(context.Background(), pid, name, "manual", "bls"); !errors.Is(err, ErrDataNotKnown) {
			t.Fatalf("%s: %v, want not known", name, err)
		}
		if r, err := worker.Backup(context.Background(), "nb/"+name, nil); err == nil {
			t.Fatalf("%s: the scheduled backup passed: %v", name, r)
		}
		if p, err := s.DeploymentProtection(context.Background(), pid, name); err != nil || p.Unknown == "" {
			t.Fatalf("%s: protection %+v %v", name, p, err)
		}
	}
	unknown("fresh") // no release recorded
	releaseMain(t, s, "nb", "app: [not a contract")
	unknown("main")

	// Declared, and none of it on the host.
	releaseMain(t, s, "nb", "app:\n  web:\n    image: x:1\n    public: true\n    dataDir: /data\n")
	hostMounts(client, nil, func(line string) ([]byte, error, bool) {
		if strings.HasPrefix(line, "volume inspect ") {
			return []byte("Error: No such volume"), errors.New("exit 1"), true
		}
		return nil, nil, false
	})
	if _, err := s.TakeBackup(context.Background(), pid, "main", "manual", "bls"); !errors.Is(err, ErrHeldDataAbsent) {
		t.Fatalf("declared and absent: %v", err)
	}

	releaseMain(t, s, "nb", "app:\n  web:\n    image: x:1\n    public: true\n")
	if _, err := s.TakeBackup(context.Background(), pid, "main", "manual", "bls"); !errors.Is(err, ErrNothingToBackUp) {
		t.Fatalf("nothing named: %v", err)
	}
	if r, err := worker.Backup(context.Background(), "nb/main", nil); err != nil || r["skipped"] == nil {
		t.Fatalf("a scheduled backup of nothing is skipped: %v %v", r, err)
	}
}

// A host that cannot be asked whether the declared data is there leaves
// what the deployment holds unknown: a backup fails as not known, the
// scheduled one fails rather than skipping, and protection reads unknown,
// never protected and never nothing to back up.
//
// Kills: taking a host that did not answer as one that holds nothing.
func TestAHostThatCannotBeAskedLeavesItsDataUnknown(t *testing.T) {
	t.Parallel()
	s, db, client, _, pid := backupWorld(t, "uh", 40021)
	releaseMain(t, s, "uh", "app:\n  web:\n    image: x:1\n    public: true\n    dataDir: /data\n")
	hostMounts(client, nil, func(line string) ([]byte, error, bool) {
		if strings.HasPrefix(line, "volume inspect ") {
			return []byte("Cannot connect to the Docker daemon at unix:///var/run/docker.sock"), errors.New("exit 1"), true
		}
		return nil, nil, false
	})
	if _, err := s.TakeBackup(context.Background(), pid, "main", "manual", "bls"); !errors.Is(err, ErrDataNotKnown) {
		t.Fatalf("an unreachable host's backup: %v, want not known", err)
	}
	worker := JobWorker{Service: s.Service, Projects: project.New(db, nil)}
	if r, err := worker.Backup(context.Background(), "uh/main", nil); err == nil {
		t.Fatalf("the scheduled backup of an unreachable host passed: %v", r)
	}
	p, err := s.DeploymentProtection(context.Background(), pid, "main")
	if err != nil || p.Unknown == "" || len(p.Items) != 0 {
		t.Fatalf("protection of an unreachable host: %+v %v", p, err)
	}
}

// twoOfEachWorld is main holding two app volumes and two Postgres volumes:
// the release's own, and an older pair still on its list (as a slug change
// and a release leave them), the old Postgres still running.
func twoOfEachWorld(t *testing.T, slug string, port int) (*both, *dockerfake.Daemon, *deployment.Deployment, *hostLog) {
	t.Helper()
	s, _, client, _, pid := backupWorld(t, slug, port)
	dep := mainOf(t, s, pid)
	record(t, s, dep,
		deploy.Owned{Kind: "volume", ID: "apptown-data-old", Data: true},
		deploy.Owned{Kind: "container", ID: "c-oldpg", Need: "postgres"},
		deploy.Owned{Kind: "volume", ID: "apptown-need-old-postgres", Data: true, Need: "postgres"})
	client.SetRunning("c-oldpg", true)
	stack := docker.StackID(slug, "main")
	hostMounts(client, map[string]string{
		"c-oldpg": "apptown-need-old-postgres\t/var/lib/postgresql/data",
		docker.Current.Service(stack, "postgres"): docker.Current.NeedVolume(stack, "postgres") + "\t/var/lib/postgresql/data",
	}, nil)
	h := &hostLog{}
	h.answer(client)
	return s, client, dep, h
}

// Two volumes of one role are two copies, each its own file and bytes;
// each goes back into its own volume; each protection row shows its own
// copy.
//
// Kills: one file for a role (a copy written over another); restoring by
// role in list order; a row borrowing another volume's copy.
func TestTwoVolumesOfOneRoleStayApart(t *testing.T) {
	t.Parallel()
	s, _, dep, h := twoOfEachWorld(t, "two", 40021)
	b, err := s.TakeBackup(context.Background(), dep.ProjectID, dep.Name, "manual", "bls")
	if err != nil || b.Status != BackupOK || len(b.Items) != 4 {
		t.Fatalf("take: %+v %v", b, err)
	}
	digest := map[string]string{}
	for _, it := range b.Items {
		if prior, dup := digest[it.SHA256]; dup {
			t.Fatalf("%s and %s are one copy", prior, it.Volume)
		}
		digest[it.SHA256] = it.Volume
	}
	p, err := s.DeploymentProtection(context.Background(), dep.ProjectID, dep.Name)
	if err != nil || len(p.Items) != 4 {
		t.Fatalf("protection: %+v %v", p, err)
	}
	for _, row := range p.Items {
		if row.LastCopy == nil || digest[row.LastCopy.SHA256] != row.Volume {
			t.Fatalf("the row for %s shows %+v", row.Volume, row.LastCopy)
		}
	}
	if _, err := s.restoreBackup(context.Background(), nil, dep.ProjectID, dep.Name, b.ID, "bls"); err != nil {
		t.Fatal(err)
	}
	got := h.restored()
	for _, want := range []string{
		"copy of apptown-data-old.tar.gz <- run --rm -i --entrypoint  --network none -v apptown-data-old:/dst",
		"copy of apptown-data-apptown-env-two-main.tar.gz <- run --rm -i --entrypoint  --network none -v apptown-data-apptown-env-two-main:/dst",
		"copy of apptown-need-old-postgres.dump <- exec -i " + pgConnection + " c-oldpg sh -c pg_restore",
		"copy of apptown-need-env-two-main-postgres.dump <- exec -i " + pgConnection + " apptown-svc-env-two-main-postgres sh -c pg_restore",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("want %q among the restores:\n%s", want, got)
		}
	}
}

// legacySet is a set as taken before items named their volumes: one app
// copy and one Postgres copy, by role only.
func legacySet(t *testing.T, s *both, dep *deployment.Deployment, slug string) *Backup {
	t.Helper()
	plan := []backupPlanItem{
		{Kind: "volume", Name: "apptown-data-gone", File: "data.tar.gz", Copy: CopyLiveFiles, Step: docker.Step{Image: "x"}},
		{Kind: "need", Name: "postgres", File: "postgres.dump", Restore: "pg_restore", Copy: CopyDeclared, Step: docker.Step{Exec: "x"}},
	}
	b, err := s.takeSet(context.Background(), docker.Scope{Client: s.hosts.Local().Client}, dep.ProjectID, slug, dep.Name, plan, "manual", "bls")
	if err != nil || b.Status != BackupOK {
		t.Fatalf("legacy set: %+v %v", b, err)
	}
	return b
}

// An older set's copies name no volume here: each goes by its role only
// when the set holds one copy of it and the deployment one volume of it.
// Two volumes of the role refuse the restore before anything changes.
//
// Kills: choosing a destination by list order.
func TestARoleOnlySetRestoresOnlyWhenUnambiguous(t *testing.T) {
	t.Parallel()
	t.Run("one of each", func(t *testing.T) {
		t.Parallel()
		s, _, _, _, pid := backupWorld(t, "leg", 40022)
		dep := mainOf(t, s, pid)
		var h hostLog
		h.answer(s.hosts.Local().Client.(*dockerfake.Daemon))
		b := legacySet(t, s, dep, "leg")
		if _, err := s.restoreBackup(context.Background(), nil, pid, "main", b.ID, "bls"); err != nil {
			t.Fatal(err)
		}
		if got := h.restored(); !strings.Contains(got, "copy of data.tar.gz <- run --rm -i --entrypoint  --network none -v apptown-data-apptown-env-leg-main:/dst") ||
			!strings.Contains(got, "copy of postgres.dump <- exec -i "+pgConnection+" apptown-svc-env-leg-main-postgres sh -c pg_restore") {
			t.Fatalf("each copy into the one volume of its role:\n%s", got)
		}
	})
	t.Run("two of each", func(t *testing.T) {
		t.Parallel()
		s, client, dep, h := twoOfEachWorld(t, "amb", 40023)
		b := legacySet(t, s, dep, "amb")
		client.ResetCommands()
		_, err := s.restoreBackup(context.Background(), nil, dep.ProjectID, dep.Name, b.ID, "bls")
		if !errors.Is(err, ErrRestoreMismatch) || !strings.Contains(err.Error(), "not certain which") {
			t.Fatalf("an ambiguous restore: %v", err)
		}
		if h.restored() != "" || indexOf(commandsOf(client), "stop ") >= 0 {
			t.Fatalf("something changed before the refusal:\n%s", strings.Join(commandsOf(client), "\n"))
		}
		if sets, err := s.backups.ForDeployment(context.Background(), dep.ProjectID, dep.Name, 10); err != nil || len(sets) != 1 {
			t.Fatalf("a safety set was taken before the refusal: %d %v", len(sets), err)
		}
	})
}

// A need made before needs had named volumes keeps its data in an
// anonymous volume; one the inspection put on the list and the release
// also reaches is planned once, by its real identity.
//
// Kills: merging before resolving the anonymous volume's identity.
func TestAnAnonymousNeedVolumeIsPlannedOnce(t *testing.T) {
	t.Parallel()
	s, _, client, _, pid := backupWorld(t, "anon", 40024)
	dep := mainOf(t, s, pid)
	svc := docker.Current.Service(docker.StackID("anon", "main"), "postgres")
	record(t, s, dep, deploy.Owned{Kind: "volume", ID: "4f9a", Data: true, Need: "postgres"}, deploy.Owned{Kind: "container", ID: svc, Need: "postgres"})
	hostMounts(client, map[string]string{svc: "4f9a\t/var/lib/postgresql/data"}, func(line string) ([]byte, error, bool) {
		if line == "volume inspect apptown-need-env-anon-main-postgres" {
			return []byte("Error: No such volume"), errors.New("exit 1"), true
		}
		return nil, nil, false
	})
	plan, err := s.heldPlan(t.Context(), pid, "anon", "main")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, it := range plan {
		if it.Name == "postgres" {
			n++
			if it.Volume != "4f9a" || it.Unresolved != "" {
				t.Fatalf("the anonymous volume's item: %+v", it)
			}
		}
	}
	if n != 1 {
		t.Fatalf("Postgres planned %d times: %+v", n, plan)
	}
}

// A declared need whose named volume is absent and whose container mounts
// nothing where its kind keeps data has no identity AppTown can name: it
// is an item not copied, each need its own, so no ok set omits one.
//
// Kills: copying the container as if it were the volume; merging such
// needs on the empty identity.
func TestANeedWithNoNamedDataIsNotCopiedAndNeverMerged(t *testing.T) {
	t.Parallel()
	for _, needs := range [][]string{{"postgres"}, {"postgres", "redis"}} {
		t.Run(strings.Join(needs, "+"), func(t *testing.T) {
			t.Parallel()
			slug := "nid" + fmt.Sprint(len(needs))
			s, _, client, _, pid := backupWorld(t, slug, 40030+len(needs))
			yaml := "app:\n  web:\n    image: x:1\n    public: true\n    dataDir: /data\nneeds:\n"
			for _, n := range needs {
				yaml += "  " + n + ": {}\n"
			}
			releaseMain(t, s, slug, yaml)
			// Older services: their named volumes absent, nothing mounted.
			hostMounts(client, nil, func(line string) ([]byte, error, bool) {
				if strings.HasPrefix(line, "volume inspect apptown-need-") {
					return []byte("Error: No such volume"), errors.New("exit 1"), true
				}
				return nil, nil, false
			})
			var h hostLog
			h.answer(client)
			b, err := s.TakeBackup(context.Background(), pid, "main", "manual", "bls")
			if err != nil || b.Status != BackupPartial || len(b.Items) != 1+len(needs) {
				t.Fatalf("take: %+v %v", b, err)
			}
			for _, it := range b.Items {
				if it.Kind == "need" && !strings.Contains(it.Error, "mounts no volume") {
					t.Fatalf("%s: %+v", it.Name, it)
				}
			}
			p, err := s.DeploymentProtection(context.Background(), pid, "main")
			if err != nil || len(p.Items) != 1+len(needs) {
				t.Fatalf("protection: %+v %v", p, err)
			}
		})
	}
}

// A copy of a known volume is evidence for that volume only: once the need
// keeps its data in another volume (an older anonymous one replaced by the
// named one), the new volume has no copy until it is copied. A copy with no
// volume recorded still counts for the one volume of its role.
//
// Kills: crediting a known other volume's copy by role.
func TestACopyOfAnotherVolumeIsNotThisVolumesCopy(t *testing.T) {
	t.Parallel()
	s, _, client, _, pid := backupWorld(t, "rep", 40035)
	svc := docker.Current.Service(docker.StackID("rep", "main"), "postgres")
	named := docker.Current.NeedVolume(docker.StackID("rep", "main"), "postgres")
	hostMounts(client, map[string]string{svc: "old-anon\t/var/lib/postgresql/data"}, func(line string) ([]byte, error, bool) {
		if line == "volume inspect "+named {
			return []byte("Error: No such volume"), errors.New("exit 1"), true
		}
		return nil, nil, false
	})
	var h hostLog
	h.answer(client)
	b, err := s.TakeBackup(context.Background(), pid, "main", "manual", "bls")
	if err != nil || b.Status != BackupOK || b.GoodCopy("need", "postgres") == nil || b.GoodCopy("need", "postgres").Volume != "old-anon" {
		t.Fatalf("the old volume's copy: %+v %v", b, err)
	}
	// The service is made again on its named volume.
	hostMounts(client, map[string]string{svc: named + "\t/var/lib/postgresql/data"}, nil)
	row := func() ItemProtection {
		t.Helper()
		p, err := s.DeploymentProtection(context.Background(), pid, "main")
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range p.Items {
			if r.Need == "postgres" {
				return r
			}
		}
		t.Fatalf("no postgres row: %+v", p.Items)
		return ItemProtection{}
	}
	if r := row(); r.Volume != named || r.LastCopy != nil {
		t.Fatalf("the new volume borrowed the old one's copy: %+v", r)
	}
	// A set from before items named their volume counts by role.
	legacySet(t, s, mainOf(t, s, pid), "rep")
	if r := row(); r.LastCopy == nil {
		t.Fatalf("a role-only copy of the one Postgres volume: %+v", r)
	}
}
