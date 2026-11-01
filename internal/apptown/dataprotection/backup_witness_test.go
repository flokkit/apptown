package dataprotection

import (
	"context"
	"io/fs"

	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/flokkit/apptown/stock"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
)

// fileCopyKind is a custom kind that keeps local data and declares a copy
// with no export, only what a file copy of it leaves out: AppTown's tar of
// its data directory. No stock kind declares one since object storage
// declared its export.
const fileCopyKind = "capability: notes\nlocal:\n  image: notes:1\n  data: /notes\ncopy:\n  caveat: the notes index is not in the copy\n"

func withFileCopyKind(t *testing.T, s *both) {
	t.Helper()
	if _, err := s.catalog.Save(t.Context(), fileCopyKind, "bls"); err != nil {
		t.Fatal(err)
	}
}

// A set records how each item was copied: a need with a declared command
// (Redis's SAVE, Postgres's dump, object storage's copy through its API) as
// declared, a need whose copy is a file copy and the app's volume, both copied
// while running, as file copies that may be inconsistent. Redis's copy is
// taken with SAVE first, so it is the data in memory now, not the last
// snapshot on disk. Object storage's copy runs in its kind's client image
// beside the store, sharing its network, given the connection the store
// was started with: its gateway's image has no copy tool.
func TestABackupSaysHowEachItemWasCopied(t *testing.T) {
	t.Parallel()
	s, _, client, _, pid := backupWorld(t, "bwc", 40010)
	withFileCopyKind(t, s)
	releaseMain(t, s, "bwc", "app:\n  web:\n    image: registry.example/bwc:1\n    ports: [\"8080\"]\n    public: true\n    dataDir: /data\nneeds:\n  redis: {}\n  s3: {}\n  notes: {}\n")
	var ran []string
	envOf := map[string][]string{}
	setBackupDockerCommand(client, func(req docker.Command) ([]byte, error) {
		line := strings.Join(req.Args, " ")
		ran = append(ran, line)
		envOf[line] = req.Env
		_, err := containerresulttest.WriteString(req.Stdout, "copy")
		return nil, err
	})
	b, err := s.TakeBackup(context.Background(), pid, "main", "bls")
	if err != nil || b.Status != BackupOK || len(b.Items) != 4 {
		t.Fatalf("take: %+v %v", b, err)
	}
	got := map[string]CopyMethod{}
	for _, it := range b.Items {
		got[it.Kind+":"+it.Name] = it.Copy
	}
	if got["need:redis"] != CopyDeclared || got["need:s3"] != CopyDeclared || got["need:notes"] != copyLiveFiles || got["volume:"+deployedNames(t, s, pid, "main", "bwc").DataVolume()] != copyLiveFiles {
		t.Fatalf("copy methods: %v", got)
	}
	// A kind's declared caveat travels with its copy; object storage's says
	// what its copy leaves out, and it is a copy that fills a new store.
	for _, it := range b.Items {
		if it.Name == "s3" && (!strings.Contains(it.Caveat, "ACLs") || it.Format != "s3-objects" || it.Client != "python:3.13.16-alpine3.24") {
			t.Fatalf("the s3 copy: %+v", it)
		}
		if (it.Name == "notes") != strings.Contains(it.Caveat, "index") {
			t.Fatalf("only the notes copy carries its kind's caveat: %+v", it)
		}
	}
	var s3 string
	for _, line := range ran {
		if strings.Contains(line, "$S3_COPY_PROGRAM\" export") {
			s3 = line
		}
	}
	if !strings.HasPrefix(s3, "run --rm --entrypoint  --network container:"+deployedNames(t, s, pid, "main", "bwc").Service("s3")+" -e S3_ACCESS_KEY_ID ") || !strings.Contains(s3, " python:3.13.16-alpine3.24 sh -c ") {
		t.Fatalf("the s3 copy runs in its client image beside the store:\n%s", strings.Join(ran, "\n"))
	}
	env := strings.Join(envOf[s3], " ")
	// The store's own connection is the deployment's generated key, as the
	// store was started with it, never the catalogue's constant.
	dep, err := s.Deployments.ByName(t.Context(), pid, "main")
	if err != nil {
		t.Fatal(err)
	}
	creds := s.Service.env.NeedCredentials(deploymentNeedScope(dep.ID))
	value := func(name string) string {
		v, err := creds(t.Context(), "s3", name)
		if err != nil || v == "" {
			t.Fatalf("the deployment's s3 %s: %q %v", name, v, err)
		}
		return v
	}
	for _, want := range []string{"S3_HOST=127.0.0.1", "S3_PORT=9000", "S3_TLS=disable", "S3_BUCKET=app", "S3_ACCESS_KEY_ID=" + value("user"), "S3_SECRET_ACCESS_KEY=" + value("password"), "S3_COPY_PROGRAM=import "} {
		if !strings.Contains(env, want) {
			t.Fatalf("the s3 copy is given the store's own connection and its program, want %s: %q", want, env)
		}
	}
	for _, never := range []string{"forge-s3-root", "=local ", "localsecret", value("root_key"), value("root_secret")} {
		if strings.Contains(env, never) {
			t.Fatalf("the s3 copy is never given the gateway's root key or the catalogue's constants (%s): %q", never, env)
		}
	}
	if !strings.Contains(strings.Join(ran, "\n"), "exec "+dockerfake.ContainerID(deployedNames(t, s, pid, "main", "bwc").Service("redis"))+" sh -c [ \"$(redis-cli SAVE)\" = OK ] && cat /data/dump.rdb") {
		t.Fatalf("Redis is made to save before its snapshot is copied:\n%s", strings.Join(ran, "\n"))
	}
	// The record, read back, keeps how and how to put it back.
	sets, err := s.backups.ForDeployment(context.Background(), pid, "main", 1)
	if err != nil || len(sets) != 1 {
		t.Fatal(sets, err)
	}
	redis, err := s.catalog.ByCapability(t.Context(), "redis")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range sets[0].Items {
		if it.Name == "redis" && it.EngineImageID != dockerfake.ImageID(redis.ResolveLocal(nil, "", nil).Image) {
			t.Fatalf("physical copy lacks its observed engine: %+v", it)
		}
		if it.Name == "redis" && (it.Copy != CopyDeclared || !it.RestoreStopped || it.Verify == "") {
			t.Fatalf("the redis item carries its restore mode and check: %+v", it)
		}
	}
}

// A restore says what witnessed each item: Redis is restored with its
// container stopped (stop, a throwaway container of its image on its
// volumes, start) and then its declared check runs; Postgres is restored
// in its running container and checked by its kind's copy.verify, both
// given the connection that container serves (its own database and user,
// never the defaults, and AppTown's password); MinIO on the tar fallback goes back as
// files nobody checked. The whole restore is "not verified", never
// "restored", and the audit receipt says the same. When a check fails the
// item failed and the restore is partial.
func TestARestoreReportsWhatWitnessedEachItem(t *testing.T) {
	for _, scenario := range []string{"check passes", "check fails"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s, db, client, _, pid := backupWorld(t, "bww", answeringPort(t))
			withFileCopyKind(t, s)
			releaseMain(t, s, "bww", "app:\n  web:\n    image: registry.example/bww:1\n    ports: [\"8080\"]\n    public: true\nneeds:\n  redis: {}\n  s3: {}\n  notes: {}\n  postgres: {database: ledger, user: keeper}\n")
			var calls, s3Steps []string
			pgEnv := map[string][]string{}
			setBackupDockerCommand(client, func(req docker.Command) ([]byte, error) {
				line := strings.Join(req.Args, " ")
				switch {
				case req.Stdout != nil:
					_, err := containerresulttest.WriteString(req.Stdout, "copy of "+filepath.Base(containerresulttest.Destination(req.Stdout).(*os.File).Name()))
					return nil, err
				case strings.HasPrefix(line, "inspect --format {{.Config.Image}}"):
					return []byte("redis:7-alpine\n"), nil
				}
				calls = append(calls, line)
				if req.Stdin != nil {
					if _, err := io.ReadAll(req.Stdin); err != nil {
						return nil, err
					}
					if strings.HasPrefix(line, "exec -i "+pgConnection+" "+deployedNames(t, s, pid, "main", "bww").Service("postgres")+" sh -c ") {
						step := "restore"
						if strings.Contains(line, "pg_restore -l") {
							step = "verify"
						}
						pgEnv[step] = req.Env
					}
					if strings.Contains(line, "$S3_COPY_PROGRAM") {
						s3Steps = append(s3Steps, line)
					}
					if scenario == "check fails" && (strings.Contains(line, "redis-check-rdb") || strings.Contains(line, "pg_restore -l") || strings.Contains(line, "$S3_COPY_PROGRAM\" verify")) {
						return []byte("loaded 0 keys"), errors.New("exit status 1")
					}
				}
				return nil, nil
			})
			b, err := s.TakeBackup(context.Background(), pid, "main", "bls")
			if err != nil || b.Status != BackupOK {
				t.Fatalf("the set: %+v %v", b, err)
			}
			client.SetRunning(deployedNames(t, s, pid, "main", "bww").App(), true) // the app runs, so it is stopped and started again
			op, res := restoreThroughOperation(t, s, pid, "main", b.ID)
			outcomes := map[string]RestoreOutcome{}
			for _, it := range res.Items {
				outcomes[it.Name] = it.Outcome
			}
			if outcomes["notes"] != restoreCopied {
				t.Fatalf("the tar fallback is files copied, not verified: %+v", res.Items)
			}
			// The known loss is stated with the restored item, not hidden
			// behind "not verified".
			for _, it := range res.Items {
				if (it.Name == "notes") != strings.Contains(it.Caveat, "index") {
					t.Fatalf("the notes item states its kind's caveat: %+v", it)
				}
			}
			// Object storage goes back by its kind's restore, in its client
			// image beside the store, and is then held to the copy by verify.
			if len(s3Steps) != 2 || !strings.HasPrefix(s3Steps[0], "run --rm -i --entrypoint  --network container:"+deployedNames(t, s, pid, "main", "bww").Service("s3")+" ") || !strings.HasSuffix(s3Steps[0], "\" restore") || !strings.HasSuffix(s3Steps[1], "\" verify") {
				t.Fatalf("the s3 restore and its check:\n%s", strings.Join(s3Steps, "\n"))
			}
			if scenario == "check passes" {
				if op.State != operations.Succeeded || res.Status != restoreNotVerified || outcomes["redis"] != restoreVerified || outcomes["postgres"] != restoreVerified || outcomes["s3"] != restoreVerified {
					t.Fatalf("restore: %+v %s %s", res, op.State, op.Detail)
				}
			} else if op.State != operations.Failed || res.Status != restorePartial || outcomes["redis"] != restoreFailed || outcomes["postgres"] != restoreFailed || outcomes["s3"] != restoreFailed {
				t.Fatalf("a failed check is a failed item: %+v %s %s", res, op.State, op.Detail)
			}
			// The password is the one the container was started with: the
			// deployment's own, never the catalogue's.
			dep, err := s.Deployments.ByName(t.Context(), pid, "main")
			if err != nil {
				t.Fatal(err)
			}
			password, err := s.Service.env.NeedCredentials(deploymentNeedScope(dep.ID))(t.Context(), "postgres", "password")
			if err != nil || password == "" || password == "app" {
				t.Fatalf("the deployment's Postgres password: %q %v", password, err)
			}
			for _, step := range []string{"restore", "verify"} {
				env := strings.Join(pgEnv[step], " ")
				for _, want := range []string{"PGHOST=127.0.0.1", "PGDATABASE=ledger", "PGUSER=keeper", "PGPASSWORD=" + password} {
					if !strings.Contains(env, want) {
						t.Fatalf("the Postgres %s is given the connection its container serves, want %s: %q", step, want, env)
					}
				}
			}
			// The order is the guarantee: Redis reads dump.rdb only when it
			// starts, and is checked only after it has.
			var redis []string
			for _, c := range calls {
				if strings.Contains(c, deployedNames(t, s, pid, "main", "bww").Service("redis")) && !strings.HasPrefix(c, "inspect ") {
					redis = append(redis, c)
				}
			}
			if len(redis) != 4 || redis[0] != "stop "+deployedNames(t, s, pid, "main", "bww").Service("redis") ||
				!strings.HasPrefix(redis[1], "run --rm -i --entrypoint  --network none --volumes-from "+deployedNames(t, s, pid, "main", "bww").Service("redis")+" redis:7-alpine sh -c ") ||
				redis[2] != "start "+deployedNames(t, s, pid, "main", "bww").Service("redis") || !strings.HasPrefix(redis[3], "exec -i "+deployedNames(t, s, pid, "main", "bww").Service("redis")+" sh -c ") {
				t.Fatalf("redis is stopped, restored beside, started, then checked:\n%s", strings.Join(redis, "\n"))
			}
			receipts, err := audit.Recent(context.Background(), db, audit.Filter{ProjectID: pid, KindPrefix: "restored"})
			if err != nil || len(receipts) != 1 || !strings.Contains(string(receipts[0].Payload), `"status":"`+res.Status.String()+`"`) || !strings.Contains(string(receipts[0].Payload), `"outcome":"copied"`) || !strings.Contains(string(receipts[0].Payload), `"caveat":"the notes index`) {
				t.Fatalf("the receipt says what the restore can claim: %+v %v", receipts, err)
			}
		})
	}
}

// A set from before sets said how they were copied claims nothing: its
// items go back as copied files, not verified.
func TestARestoreOfAnOldSetClaimsNothing(t *testing.T) {
	t.Parallel()
	plan := backupPlanItem{Kind: "need", Name: "postgres", Container: "pg", Restore: "pg_restore"}
	s, _, client, dir, _ := backupWorld(t, "bwo", 40012)
	setBackupDockerCommand(client, func(req docker.Command) ([]byte, error) { return nil, nil })
	path := filepath.Join(dir, "postgres.dump")
	if err := os.WriteFile(path, []byte("dump"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := &Backup{Path: dir, Items: []BackupItem{{Kind: "need", Name: "postgres", File: "postgres.dump", Restore: "pg_restore", SHA256: fileSHA256(path)}}}
	row := s.restoreItem(context.Background(), nil, docker.Scope{Client: client}, restoreMove{item: b.Items[0], into: plan}, filepath.Join(b.Path, b.Items[0].File), "test-helper")
	if row.Outcome != restoreCopied {
		t.Fatalf("an old set's item: %+v", row)
	}
}

// A set of object storage taken before its kind declared a copy holds a
// tar of the store's files: it still goes back by the command it records,
// in the store's own container where those files are (never in the copy's
// client image, which has no files), and says it was files copied, not
// verified.
func TestAnObjectStoreTarSetGoesBackAsFilesInTheStore(t *testing.T) {
	t.Parallel()
	s, _, client, dir, pid := backupWorld(t, "bwt", 40014)
	kind, err := s.catalog.ByCapability(t.Context(), "s3")
	if err != nil {
		t.Fatal(err)
	}
	var ran []string
	setBackupDockerCommand(client, func(req docker.Command) ([]byte, error) {
		ran = append(ran, strings.Join(req.Args, " "))
		return nil, nil
	})
	path := filepath.Join(dir, "s3.tar.gz")
	if err := os.WriteFile(path, []byte("files"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := BackupItem{Kind: "need", Name: "s3", File: "s3.tar.gz", Restore: "tar xz -C /data", Copy: copyLiveFiles, Caveat: "object metadata is not in the copy", SHA256: fileSHA256(path)}
	into := needItem(kind, "s3", deployedNames(t, s, pid, "main", "bwt").Service("s3"))
	row := s.restoreItem(context.Background(), nil, docker.Scope{Client: client}, restoreMove{item: old, into: into}, path, "test-helper")
	if row.Outcome != restoreCopied || !strings.Contains(row.Detail, "not verified") || !strings.Contains(row.Caveat, "metadata") {
		t.Fatalf("an old tar set of a store: %+v", row)
	}
	var restored []string
	for _, line := range ran {
		if strings.Contains(line, "tar xz") {
			restored = append(restored, line)
		}
	}
	if len(restored) != 1 || restored[0] != "exec -i "+deployedNames(t, s, pid, "main", "bwt").Service("s3")+" sh -c tar xz -C /data" {
		t.Fatalf("the files go back in the store's own container:\n%s", strings.Join(ran, "\n"))
	}
}

// A declared need whose kind cannot be read (a shipped file that stopped
// parsing, or a need not in the catalog) is never left out of the plan,
// which would make the set "ok" without its data. The set records it as not copied, with why, and is partial; the plan
// says so; and a restore into that deployment is refused before anything
// changes.
func TestABackupNamesDeclaredNeedsItCannotRead(t *testing.T) {
	t.Parallel()
	s, _, client, _, pid := backupWorld(t, "bwu", 40013)
	// The catalog's own sync stores the shipped file unreadable. A sync
	// removes the stock kinds it is not given, so it is given Postgres too.
	pg, err := fs.ReadFile(stock.FS, "dependencies/postgres.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.catalog.SyncStock(context.Background(), fstest.MapFS{"dependencies/postgres.yaml": {Data: pg}, "dependencies/kv.yaml": {Data: []byte("capability: kv\nlocal:\n  image: kv:1\n  data: /data\ncopy:\n  ext: f\n  export: cat /data/f\n  verify: cmp -s - /data/f\n")}}, "dependencies"); !errors.Is(err, dependency.ErrStockUnreadable) {
		t.Fatalf("the broken kind: %v", err)
	}
	releaseMain(t, s, "bwu", "app:\n  web:\n    image: registry.example/bwu:1\n    ports: [\"8080\"]\n    public: true\nneeds:\n  postgres: {}\n  kv: {}\n  quantum: {}\n")
	names := hostNamesOf(t, mainOf(t, s, pid), "bwu")
	for _, need := range []string{"kv", "quantum"} {
		client.SetVolume(names.NeedVolume(need), true)
	}
	var writes []string
	setBackupDockerCommand(client, func(req docker.Command) ([]byte, error) {
		if req.Stdin != nil {
			writes = append(writes, strings.Join(req.Args, " "))
		}
		if req.Stdout != nil {
			_, err := containerresulttest.WriteString(req.Stdout, "copy")
			return nil, err
		}
		return nil, nil
	})
	b, err := s.TakeBackup(context.Background(), pid, "main", "bls")
	if err != nil || b.Status != BackupPartial || len(b.Items) != 3 {
		t.Fatalf("a set missing a declared need's data is partial: %+v %v", b, err)
	}
	why := map[string]string{}
	for _, it := range b.Items {
		why[it.Name] = it.Error
	}
	if why["postgres"] != "" || !strings.Contains(why["kv"], "cannot be read") || !strings.Contains(why["kv"], "no restore") || !strings.Contains(why["quantum"], "not in the dependency catalog") {
		t.Fatalf("each unread need says why: %v", why)
	}
	collectProtectionForTest(t, s, pid, "main")
	plan, _ := s.BackupPlan(t.Context(), pid, "bwu", "main")
	notCopied := 0
	for _, it := range plan {
		if strings.HasPrefix(it.How, "not copied: ") {
			notCopied++
		}
	}
	if len(plan) != 3 || notCopied != 2 {
		t.Fatalf("the plan says what it cannot copy: %+v", plan)
	}
	if _, err := s.restoreBackup(context.Background(), nil, pid, "main", b.ID, "bls"); !errors.Is(err, errRestoreMismatch) || !strings.Contains(err.Error(), "kv") || len(writes) != 0 {
		t.Fatalf("a restore into a deployment with an unread need is refused before any write: %v %v", err, writes)
	}
}

// A kind that declares no copy is not copied by some other means: its set
// says the need is not in it, nothing is run to copy it, and a restore of
// that deployment is refused naming it, before anything is changed.
func TestANeedWhoseKindDeclaresNoCopyIsNotInTheSet(t *testing.T) {
	t.Parallel()
	s, _, client, _, pid := backupWorld(t, "bnc", 40016)
	if _, err := s.catalog.Save(t.Context(), "capability: ledger\nlocal:\n  image: ledger:1\n  data: /ledger\n", "bls"); err != nil {
		t.Fatal(err)
	}
	releaseMain(t, s, "bnc", "app:\n  web:\n    image: registry.example/bnc:1\n    ports: [\"8080\"]\n    public: true\nneeds:\n  redis: {}\n  ledger: {}\n")
	var ran []string
	setBackupDockerCommand(client, func(req docker.Command) ([]byte, error) {
		ran = append(ran, strings.Join(req.Args, " "))
		if req.Stdout != nil {
			_, err := containerresulttest.WriteString(req.Stdout, "copy")
			return nil, err
		}
		return nil, nil
	})
	b, err := s.TakeBackup(context.Background(), pid, "main", "bls")
	if err != nil {
		t.Fatal(err)
	}
	var ledger *BackupItem
	for i := range b.Items {
		if b.Items[i].Name == "ledger" {
			ledger = &b.Items[i]
		}
	}
	if b.Status == BackupOK || ledger == nil || !strings.Contains(ledger.Error, "declares no copy") {
		t.Fatalf("the set does not say the ledger is not in it: %s %+v", b.Status, b.Items)
	}
	for _, line := range ran {
		if strings.Contains(line, "/ledger") {
			t.Fatalf("the ledger was copied by another means: %s", line)
		}
	}
	ran = nil
	withJobs(t, s)
	if _, err := s.RequestRestore(context.Background(), pid, "main", b.ID); err == nil || !strings.Contains(err.Error(), "declares no copy") {
		t.Fatalf("a restore of a deployment with an uncopied need was admitted: %v", err)
	}
	for _, line := range ran {
		if strings.HasPrefix(line, "stop ") || strings.Contains(line, "tar x") {
			t.Fatalf("the refused restore changed something: %s", line)
		}
	}
}

// An item is put back only as its own record says: one whose record has no
// digest, or (a need) no restore command, is not restored, and nothing is
// run for it, never by the destination's command of today or unchecked.
func TestAnItemWithoutItsDigestOrRestoreCommandIsNotRestored(t *testing.T) {
	t.Parallel()
	s, _, client, dir, pid := backupWorld(t, "bwd", 40018)
	kind, err := s.catalog.ByCapability(t.Context(), "redis")
	if err != nil {
		t.Fatal(err)
	}
	var ran []string
	setBackupDockerCommand(client, func(req docker.Command) ([]byte, error) {
		ran = append(ran, strings.Join(req.Args, " "))
		return nil, nil
	})
	path := filepath.Join(dir, "redis.rdb")
	if err := os.WriteFile(path, []byte("copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	into := needItem(kind, "redis", deployedNames(t, s, pid, "main", "bwd").Service("redis"))
	for name, c := range map[string]struct {
		item BackupItem
		says string
	}{
		"no digest":          {BackupItem{Kind: "need", Name: "redis", File: "redis.rdb", Restore: into.Restore, Copy: CopyDeclared}, "no digest"},
		"no restore command": {BackupItem{Kind: "need", Name: "redis", File: "redis.rdb", Copy: CopyDeclared, SHA256: fileSHA256(path)}, "no restore command"},
	} {
		row := s.restoreItem(context.Background(), nil, docker.Scope{Client: client}, restoreMove{item: c.item, into: into}, path, "test-helper")
		if row.Outcome != restoreFailed || !strings.Contains(row.Detail, c.says) {
			t.Errorf("%s: %+v", name, row)
		}
	}
	if len(ran) != 0 {
		t.Fatalf("a refused item ran:\n%s", strings.Join(ran, "\n"))
	}
}
