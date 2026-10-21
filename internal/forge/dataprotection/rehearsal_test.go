package dataprotection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockerfake"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/operations"
)

// rehearsalContract is the release a rehearsal runs: a public web part
// with a readiness path and a data check, a worker (which a rehearsal does
// not start), a migration, and Postgres.
const rehearsalContract = `app:
  web:
    build: .
    ports: ["8080"]
    public: true
    health: { readiness: { http: /healthz } }
  worker:
    build: .
    command: ["worker"]
init:
  - name: migrate
    build: .
needs:
  postgres:
    version: "16"
rehearsal:
  checks: ["GET /api/orders?limit=1", "GET /signin expect 302"]
`

// rehearsalSpec declares a production mail password and a signing key the
// AppTown generates; the rehearsal profile names neither.
const rehearsalSpec = `[project]
name = "ledger"
revision = "1.0"
[profiles.default]
SMTP_PASSWORD = { description = "production mail" }
SECRET_KEY_BASE = { description = "signing", generate = true }
[profiles.rehearsal]
defaults = { inherit = false }
`

// rehearsalHost is the daemon of the Docker host kept for rehearsals: it
// remembers what each job was given, answers the probe by path, and fails
// what a test asks it to with output that holds a row of production data.
type rehearsalHost struct {
	*dockerfake.Daemon
	mu      sync.Mutex
	status  map[string]string // path -> status the app answers ("302 /login": a redirect)
	volumes map[string]string // the host's volumes: what was staged in each, by "<volume>/<file>"
	// objects is the new S3 store the rehearsal's s3 need starts, as its
	// import fills it; corrupt changes one of its objects after the import.
	objects  map[string]fakeObject
	corrupt  bool
	filled   string          // what the import put into the new postgres
	failWith map[string]bool // "import", "init": fail with planted output
	envs     [][]string      // each job's environment
	// started is each need the rehearsal started, by its network alias,
	// with the environment it was started with.
	started map[string]map[string]string
	// beforeImport, when set, runs before the first import reaches the
	// daemon, outside its lock: AppTown stopping there.
	beforeImport func()
	// afterStage, when set, runs once a copy has been staged on the host,
	// outside the daemon's lock.
	afterStage func()
}

// Run is the daemon's, after beforeImport.
func (h *rehearsalHost) Run(ctx context.Context, host string, req docker.Command) ([]byte, error) {
	if len(req.Args) > 0 && strings.Contains(req.Args[len(req.Args)-1], "pg_restore -d") {
		h.mu.Lock()
		hook := h.beforeImport
		h.beforeImport = nil
		h.mu.Unlock()
		if hook != nil {
			hook()
		}
	}
	out, err := h.Daemon.Run(ctx, host, req)
	if len(req.Args) > 0 && strings.Contains(req.Args[len(req.Args)-1], "cat > "+copyMount+"/") {
		h.mu.Lock()
		hook := h.afterStage
		h.afterStage = nil
		h.mu.Unlock()
		if hook != nil {
			hook()
		}
	}
	return out, err
}

// stagedFile is the copy a job's script reads from, or writes to, at the
// copy's place: "<volume>/<file>".
func stagedFile(args []string) string {
	script := args[len(args)-1]
	i := strings.Index(script, copyMount+"/")
	if i < 0 {
		return volumeOf(args)
	}
	file := strings.Fields(script[i+len(copyMount)+1:])[0]
	return volumeOf(args) + "/" + file
}

// volumeOf is the volume a job mounts at the copy's place.
func volumeOf(args []string) string {
	for i, a := range args {
		if a == "-v" && i+1 < len(args) {
			if vol, rest, _ := strings.Cut(args[i+1], ":"); strings.HasPrefix(rest, copyMount) {
				return vol
			}
		}
	}
	return ""
}

// planted is a row of production data a failing command prints.
const planted = "alice.private@example.com"

func newRehearsalHost() *rehearsalHost {
	h := &rehearsalHost{Daemon: dockerfake.New(), status: map[string]string{"/signin": "302 /login"}, volumes: map[string]string{}, objects: map[string]fakeObject{}, failWith: map[string]bool{}, started: map[string]map[string]string{}}
	h.SetReply(func(args []string) ([]byte, error, bool) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if args[0] == "image" && len(args) == 5 && args[1] == "inspect" {
			ref := args[4]
			return []byte(`sha256:` + strings.Repeat("b", 64) + ` ["` + ref + `"]`), nil, true
		}
		if args[0] == "run" && slices.Contains(args, "--init") && slices.Contains(args, docker.Current.Init("rehearsal-"+opOf(args), "migrate")) && h.failWith["init"] {
			return []byte(`ERROR: duplicate key value violates unique constraint "users_email_key" DETAIL: Key (email)=(` + planted + `) already exists.`), errors.New("exit status 1"), true
		}
		if i := slices.Index(args, "--network-alias"); args[0] == "run" && i > 0 && i+1 < len(args) {
			env := map[string]string{}
			for j, a := range args {
				if a == "-e" && j+1 < len(args) {
					k, v, _ := strings.Cut(args[j+1], "=")
					env[k] = v
				}
			}
			h.started[args[i+1]] = env
		}
		return nil, nil, false
	})
	h.SetCommandReply(func(c docker.Command) ([]byte, error, bool) {
		if len(c.Args) < 3 || c.Args[0] != "run" || c.Args[len(c.Args)-3] != "sh" {
			return nil, nil, false
		}
		script := c.Args[len(c.Args)-1]
		dep := map[string]string{}
		for _, kv := range c.Env {
			k, v, _ := strings.Cut(kv, "=")
			dep[k] = v
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		h.envs = append(h.envs, c.Env)
		switch {
		case strings.Contains(script, "PROBE_PATH"):
			status, ok := h.status[dep["PROBE_PATH"]]
			if !ok {
				status = "200"
			}
			// The app's whole answer stays in the probe's container; only
			// the status line's code (and a redirect's Location) comes out.
			code, location, _ := strings.Cut(status, " ")
			return []byte(code + "\n" + location + "\n"), nil, true
		case strings.Contains(script, "cat > "+copyMount+"/"):
			data, _ := io.ReadAll(c.Stdin)
			h.volumes[stagedFile(c.Args)] = string(data)
			return nil, nil, true
		case strings.Contains(script, "pg_restore -d"):
			if c.Stdin != nil {
				return []byte("the copy came from AppTown, not the host's volume"), errors.New("exit status 9"), true
			}
			data := h.volumes[stagedFile(c.Args)]
			if h.failWith["import"] {
				return []byte(`pg_restore: error: COPY failed for table "users": ERROR: value too long: ` + planted), errors.New("exit status 1"), true
			}
			// The new server is reached with the password it was started
			// with: the rehearsal's own, never the catalogue's.
			if pw := h.started["postgres"]["POSTGRES_PASSWORD"]; dep["PGHOST"] != "postgres" || pw == "" || pw == "app" || dep["PGPASSWORD"] != pw || dep["PGSSLMODE"] != "disable" {
				return []byte("could not connect"), errors.New("exit status 2"), true
			}
			h.filled = strings.TrimPrefix(string(data), "PGCOPY\n")
			return nil, nil, true
		case strings.Contains(script, "pg_restore -l"):
			return nil, nil, true
		case strings.Contains(script, "$S3_COPY_PROGRAM"):
			// The new store, reached on the rehearsal's network by its alias
			// and its own key, filled from the staged copy and held to it.
			store := h.started["s3"]
			if dep["S3_HOST"] != "s3" || dep["S3_PORT"] != "9000" || store["APP_ACCESS_KEY_ID"] == "" || store["APP_ACCESS_KEY_ID"] == "local" ||
				dep["S3_ACCESS_KEY_ID"] != store["APP_ACCESS_KEY_ID"] || dep["S3_SECRET_ACCESS_KEY"] != store["APP_SECRET_ACCESS_KEY"] || dep["S3_BUCKET"] != "app" {
				return []byte("the new store answered 403"), errors.New("exit status 1"), true
			}
			var in []fakeObject
			if err := json.Unmarshal([]byte(h.volumes[stagedFile(c.Args)]), &in); err != nil {
				return []byte("the copy cannot be read"), errors.New("exit status 1"), true
			}
			switch {
			case strings.HasSuffix(script, `" import`):
				if len(h.objects) > 0 {
					return []byte("the bucket already holds objects"), errors.New("exit status 1"), true
				}
				for _, o := range in {
					h.objects[o.Key] = o
				}
				if h.corrupt && len(in) > 0 {
					o := h.objects[in[0].Key]
					o.Body = append([]byte("changed "), o.Body...)
					h.objects[in[0].Key] = o
				}
			case strings.HasSuffix(script, `" verify`):
				if len(in) != len(h.objects) {
					return []byte("the bucket holds other objects than the copy"), errors.New("exit status 1"), true
				}
				for _, o := range in {
					if got, ok := h.objects[o.Key]; !ok || !bytes.Equal(got.Body, o.Body) || got.Type != o.Type {
						return []byte("objects differ from the copy"), errors.New("exit status 1"), true
					}
				}
			default:
				return nil, nil, false
			}
			return nil, nil, true
		}
		return nil, nil, false
	})
	return h
}

// opOf is the operation a rehearsal command names, from its init container.
func opOf(args []string) string {
	for _, a := range args {
		if rest, ok := strings.CutPrefix(a, "apptown-init-rehearsal-"); ok && len(rest) >= 36 {
			return rest[:36] // the operation's id
		}
	}
	return ""
}

// rehearsalWorld is a dataWorld whose production App Platform deployment
// "prod" serves a release published to a registry, holds records, and has
// one copy in the store; and a Docker host attached and kept for
// rehearsals, reached through a fake daemon.
type rehearsalWorld struct {
	*dataWorld
	host   *rehearsalHost
	target *deployment.Target
	set    ids.ID
}

func newRehearsalWorld(t *testing.T) *rehearsalWorld {
	t.Helper()
	return newRehearsalWorldWith(t, rehearsalContract, newDataWorld(t), nil)
}

// newRehearsalWorldWith is a rehearsal world whose release runs contract,
// over dw (built with a contract that makes what prod must hold); seed,
// when set, puts prod's data in place before its copy is taken.
func newRehearsalWorldWith(t *testing.T, contract string, dw *dataWorld, seed func(*dataWorld)) *rehearsalWorld {
	t.Helper()
	w := &rehearsalWorld{dataWorld: dw, host: newRehearsalHost()}
	w.protect()
	ctx := context.Background()
	repo := filepath.Join(w.s.dataDir, "workspaces", "project-ledger")
	for name, body := range map[string]string{".apptown/app.yaml": contract, "secretspec.toml": rehearsalSpec} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "rehearsal checks"}} {
		cmd := gittest.Command(args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	revision, err := releaseGit(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	digest := releaseRepo + digestA
	claim := &claimedAttempt{db: w.db, project: w.project, run: idstest.N(7), step: idstest.N(70), at: 1}
	bid, err := w.s.Bundles.Cut(ctx, w.project, strings.TrimSpace(revision), map[string]string{"web": digest, "worker": digest}, map[string]string{"migrate": digest},
		[]byte(contract), digestOrigin(claim))
	if err != nil {
		t.Fatal(err)
	}
	dep, err := w.bind(w.project, "prod", "production", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	rel, log := w.release("prod")
	if rel.State != deployment.Released {
		t.Fatalf("release %s:\n%s", rel.State, log)
	}
	// sql-guarantee: an App Platform release of a digest bundle copies its images between two registries, which this world has none of; the rehearsal reads only that the served release names a digest bundle.
	if _, err := w.db.Exec(`UPDATE releases SET bundle_id=$1 WHERE id=$2`, bid, rel.ID); err != nil {
		t.Fatal(err)
	}
	cluster, _ := w.clusterOf(dep)["id"].(string)
	w.clusters.put(cluster+".fake", "app", "orders: 41, 42")
	if seed != nil {
		seed(w.dataWorld)
	}
	op, err := w.s.RequestCopy(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.run(op); !endedAs(got, OperationNotVerified) {
		t.Fatalf("copy %s: %s", got.State, got.Detail)
	}
	set, err := w.s.newestStoreSet(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	w.set = set.ID
	if w.target, err = w.s.Targets.Put(ctx, &deployment.Target{Name: "rehearse-1", Kind: deployment.KindDockerHost, Origin: deployment.OriginAttached, Status: deployment.TargetReady,
		Address: map[string]any{"hostname": "rehearse-1.example", "user": "forge", "keyFrom": "SSH"}}); err != nil {
		t.Fatal(err)
	}
	yes := true
	if w.target, err = w.s.Targets.EditSettings(ctx, "rehearse-1", deployment.TargetSettingsEdit{ExpectedRevision: &w.target.SettingsRevision, Rehearsals: &yes}, "admin"); err != nil {
		t.Fatal(err)
	}
	w.s.hostScope = func(_ context.Context, t *deployment.Target) (docker.Scope, error) {
		if t.Name != "rehearse-1" {
			return docker.Scope{}, fmt.Errorf("rehearsed on %s", t.Name)
		}
		return docker.Scope{Client: w.host, Host: "ssh://forge@rehearse-1.example"}, nil
	}
	return w
}

// rehearse starts a rehearsal as alice and runs it as its job does.
func (w *rehearsalWorld) rehearse() *operations.Operation {
	w.t.Helper()
	op, err := w.s.RequestRehearsal(context.Background(), w.project, "prod", ids.Nil)
	if err != nil {
		w.t.Fatal(err)
	}
	return w.run(op)
}

// appRun is the docker run that started the rehearsal's app container.
func (w *rehearsalWorld) appRun(op ids.ID) []string {
	w.t.Helper()
	name := fmt.Sprintf("apptown-rehearsal-%d", op)
	for _, c := range w.host.Commands() {
		if c[0] == "run" && slices.Contains(c, "--name") && c[slices.Index(c, "--name")+1] == name {
			return c
		}
	}
	w.t.Fatalf("no app container was started: %v", w.host.Commands())
	return nil
}

// A rehearsal boots the deployment's release on its newest copy on the
// host kept for rehearsals, with no address outside that host, asks the
// app its readiness, its front page and its declared data check from inside
// the stack's network, records that on the deployment, and takes everything
// it made down (design/subsystems/deployment.md, "Restore rehearsals").
//
// Kills: publishing the app's port, joining the host's ingress, starting
// the worker, delivering the production mail password or the source's
// signing key, trusting AppTown's own origins instead of the deployment's
// address, not filling the copy before the migration, leaving the staged
// copy or the stack behind, not recording the rehearsal on the deployment.
func TestARehearsalRunsTheReleaseOnACopyWithNoAddressAndTakesItDown(t *testing.T) {
	t.Parallel()
	w := newRehearsalWorld(t)
	// The front page sends a visitor on to /welcome, on the same host: the
	// page check follows it there, on the rehearsal's own network.
	w.host.status["/"] = "302 /welcome"
	op := w.rehearse()
	if !endedAs(op, OperationVerified) {
		t.Fatalf("rehearsal %s: %s\n%+v", op.State, op.Detail, op.Steps)
	}
	if w.host.filled != "orders: 41, 42" {
		t.Fatalf("the new postgres holds %q, not the copy", w.host.filled)
	}
	run := w.appRun(op.ID)
	joined := strings.Join(run, " ")
	for _, banned := range []string{" -p ", "traefik", "--publish", "-P "} {
		if strings.Contains(" "+joined+" ", banned) {
			t.Errorf("the app was given an address outside its host (%q): %s", banned, joined)
		}
	}
	network := run[slices.Index(run, "--network")+1]
	if network != docker.Current.Network(fmt.Sprintf("rehearsal-%d", op.ID)) {
		t.Errorf("the app is on %s, not the rehearsal's own network", network)
	}
	dep, _ := w.s.Deployments.ByName(t.Context(), w.project, "prod")
	origin, _ := rehearsalOrigin(dep)
	if !slices.Contains(run, "APPTOWN_ORIGINS=https://"+origin) {
		t.Errorf("the app does not trust only the deployment's address %s: %s", origin, joined)
	}
	if strings.Contains(joined, "SMTP_PASSWORD") {
		t.Errorf("the production mail password reached the rehearsal: %s", joined)
	}
	for _, c := range w.host.Commands() {
		if c[0] == "run" && slices.Contains(c, "--name") && strings.Contains(c[slices.Index(c, "--name")+1], "worker") {
			t.Errorf("the worker was started: %v", c)
		}
	}
	ref, err := w.s.LastRehearsal(context.Background(), w.project, "prod")
	if err != nil || ref == nil {
		t.Fatalf("no rehearsal recorded: %v", err)
	}
	if ref.Outcome != RehearsalPassed || ref.Backup != w.set || ref.Host != "rehearse-1" || ref.Previous != nil {
		t.Fatalf("recorded: %+v", ref)
	}
	var names []string
	for _, c := range ref.Facts.Checks {
		names = append(names, fmt.Sprintf("%s %s %d %v", c.Name, c.Path, c.Status, c.Passed))
	}
	want := []string{"readiness /healthz 200 true", "page / 200 true", "data GET /api/orders?limit=1 /api/orders?limit=1 200 true", "data GET /signin expect 302 /signin 302 true"}
	if !slices.Equal(names, want) {
		t.Fatalf("checks %v, want %v", names, want)
	}
	if !slices.Equal(ref.Facts.Withheld, []string{"SMTP_PASSWORD"}) || !slices.Equal(ref.Facts.NotStarted, []string{"worker"}) || ref.Facts.Egress != egressNotBlocked {
		t.Fatalf("facts: %+v", ref.Facts)
	}
	// Taken down: the app with its volumes, the stack and its network, and
	// the staged copy.
	cmds := fmt.Sprint(w.host.Commands())
	for _, want := range []string{fmt.Sprintf("rm -f -v apptown-rehearsal-%d", op.ID), fmt.Sprintf("volume rm %s", docker.Current.DataVolume(fmt.Sprintf("apptown-rehearsal-%d", op.ID))), "network rm " + network} {
		if !strings.Contains(cmds, want) {
			t.Errorf("not taken down: no %q", want)
		}
	}
	if !strings.Contains(cmds, "volume rm "+placeOf(op.ID, "").Copy) {
		t.Errorf("the staged copy's volume was not removed from the host")
	}
	if op.Result["cleared"] == nil {
		t.Errorf("the rehearsal is not marked cleared: %+v", op.Result)
	}
	receipts := operationReceipts(t, w.db, "deployment.rehearsed", w.project)
	if len(receipts) != 1 || !strings.Contains(string(receipts[0].Payload), `"outcome":"passed"`) {
		t.Fatalf("receipt: %+v", receipts)
	}
	prot, err := w.s.DeploymentProtection(context.Background(), w.project, "prod")
	if err != nil || prot.Rehearsal == nil || prot.Rehearsal.Outcome != RehearsalPassed {
		t.Fatalf("protection: %+v %v", prot, err)
	}
	// A passed rehearsal from a fresh copy speaks for the items assessed,
	// never for data outside the deployment (an uploads bucket).
	if len(prot.Items) == 0 || prot.Items[0].Copy != CopyFresh || len(prot.Coverage.Assessed) != len(prot.Items) || !slices.Equal(prot.Coverage.NotAssessed, []string{OutsideNotAssessed}) {
		t.Fatalf("coverage after a passed rehearsal: %+v of %+v", prot.Coverage, prot.Items)
	}
}

// A deployment whose copy holds its bucket's objects rehearses with them:
// the set's copy of the bucket fills the new store the release's s3 need
// starts on the sealed host, by the kind's import, and its verify holds the
// store to the copy, on the rehearsal's own network and with the new
// store's own key, never the source bucket's. A Docker host's copy of a
// store fills it the same way. A store that does not hold the copy after
// the import fails the rehearsal.
func TestARehearsalFillsTheNewStoreFromTheBucketsCopy(t *testing.T) {
	contract := strings.Replace(rehearsalContract, "needs:\n  postgres:\n    version: \"16\"\n", "needs:\n  postgres:\n    version: \"16\"\n  s3: {}\n", 1)
	// prod runs a release that makes its bucket; the rehearsal runs the
	// release the rehearsal contract describes.
	prod := "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  postgres:\n    version: \"16\"\n  s3: {}\n"
	for _, scenario := range []string{"App Platform's copy", "a Docker host's copy", "a store that does not hold it"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			w := newRehearsalWorldWith(t, contract, newDataWorldWith(t, dataWorldOptions{contract: []byte(prod)}), func(dw *dataWorld) { putObjects(dw, bucketOfProd) })
			want := map[string]fakeObject{"invoices/1.pdf": {Key: "invoices/1.pdf", Type: "application/pdf", Body: []byte("%PDF one")}, "logo.png": {Key: "logo.png", Type: "image/png", Body: []byte("PNG")}}
			if scenario == "a Docker host's copy" {
				// A newer set of prod's, taken as a Docker host takes one: its
				// store's copy beside the store, its database's in its container.
				want = map[string]fakeObject{"from-docker.txt": {Key: "from-docker.txt", Type: "text/plain", Body: []byte("docker")}}
				d := dockerfake.New()
				setBackupDockerCommand(d, func(req docker.Command) ([]byte, error) {
					if req.Stdout == nil {
						return nil, nil
					}
					out := "PGCOPY\norders: 41, 42"
					if strings.Contains(strings.Join(req.Args, " "), "$S3_COPY_PROGRAM") {
						raw, _ := json.Marshal([]fakeObject{want["from-docker.txt"]})
						out = string(raw)
					}
					_, err := io.WriteString(req.Stdout, out)
					return nil, err
				})
				pg, _ := w.s.catalog.ByCapability(context.Background(), "postgres")
				s3, _ := w.s.catalog.ByCapability(context.Background(), "s3")
				set, err := w.s.takeSet(context.Background(), docker.Scope{Client: d}, w.project, "ledger", "prod", []backupPlanItem{needItem(pg, "postgres", "c-pg"), needItem(s3, "s3", "c-s3")}, "manual", "alice")
				if err != nil || set.Status != BackupOK || set.GoodCopy("need", "s3").Format != "s3-objects" {
					t.Fatalf("the Docker host's set: %+v %v", set, err)
				}
			}
			w.host.corrupt = scenario == "a store that does not hold it"
			op := w.rehearse()
			if scenario == "a store that does not hold it" {
				if endedAs(op, OperationVerified) || !strings.Contains(op.Detail, "the s3 kind's verify ended with") {
					t.Fatalf("a store that does not hold the copy: %s: %s", op.State, op.Detail)
				}
				return
			}
			if !endedAs(op, OperationVerified) {
				t.Fatalf("rehearsal %s: %s\n%+v", op.State, op.Detail, op.Steps)
			}
			w.host.mu.Lock()
			got := maps.Clone(w.host.objects)
			w.host.mu.Unlock()
			if !maps.EqualFunc(got, want, func(a, b fakeObject) bool { return a.Type == b.Type && bytes.Equal(a.Body, b.Body) }) {
				t.Fatalf("the new store holds %+v, not the copy %+v", got, want)
			}
			network := docker.Current.Network(fmt.Sprintf("rehearsal-%d", op.ID))
			for _, c := range w.host.Commands() {
				if c[0] == "run" && strings.Contains(c[len(c)-1], "$S3_COPY_PROGRAM") && c[slices.Index(c, "--network")+1] != network {
					t.Fatalf("the store was filled off the rehearsal's network: %v", c)
				}
			}
		})
	}
}

// Nothing a project reader can see of a failed rehearsal repeats what a
// command printed: a failing import, a failing migration and a failing data
// check each say the step, what ran and how it exited, and a row of
// production data in their output appears in no step, result, record,
// receipt or protection answer.
//
// A data check is judged on its own path's answer: one that redirects to a
// sign-in page fails, however that page answers.
//
// Kills: putting copyJob's or the init step's error text in a step or the
// detail; recording a response body or a container log.
func TestAFailedRehearsalRecordsOnlyStepsAndStatuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, fail, step string
	}{
		{"import", "import", "the postgres kind's import ended with exit status 1"},
		{"migration", "init", `the init step "migrate" did not finish`},
		{"data check", "check", "/api/orders?limit=1 500"},
		// A data path behind sign-in redirects there: it read no data, so
		// it fails, however the sign-in page answers.
		{"data check redirected to sign in", "redirect", "/api/orders?limit=1 302"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newRehearsalWorld(t)
			switch tc.fail {
			case "check":
				w.host.status["/api/orders?limit=1"] = "500"
			case "redirect":
				w.host.status["/api/orders?limit=1"] = "302 /login"
				w.host.status["/login"] = "200"
			default:
				w.host.failWith[tc.fail] = true
			}
			op := w.rehearse()
			if op.State != operations.Failed || !strings.Contains(op.Detail, tc.step) {
				t.Fatalf("rehearsal %s: %s", op.State, op.Detail)
			}
			ref, err := w.s.LastRehearsal(context.Background(), w.project, "prod")
			if err != nil || ref == nil || ref.Passed || ref.FailedStep == "" {
				t.Fatalf("recorded: %+v %v", ref, err)
			}
			prot, err := w.s.DeploymentProtection(context.Background(), w.project, "prod")
			if err != nil {
				t.Fatal(err)
			}
			seen, _ := json.Marshal([]any{op, ref, prot, operationReceipts(t, w.db, "deployment.", w.project)})
			if bytes.Contains(seen, []byte(planted)) {
				t.Fatalf("a row of production data reached a project reader: %s", seen)
			}
			if op.Result["cleared"] == nil {
				t.Errorf("a failed rehearsal was not taken down: %+v", op.Result)
			}
		})
	}
}

// Who may rehearse, and where: deployment:restore in the deployment's
// project; a host a platform administrator keeps for rehearsals, exactly
// one, attached and Docker; and such a host takes nothing else.
//
// Kills: a deploy grant sufficing; a built-in or provider target kept for
// rehearsals; an ordinary deployment placed on a rehearsal host; picking one
// of several kept hosts; marking a host without the meaning in its receipt.
func TestARehearsalIsRefusedWithoutAuthorityOrAPlace(t *testing.T) {
	t.Parallel()
	w := newRehearsalWorld(t)
	ctx := context.Background()

	w.auth.set("dana", "deployment:restore")
	if _, err := w.s.RequestRehearsal(ctx, w.project, "prod", ids.Nil); fault.KindOf(err) != fault.Forbidden {
		t.Fatalf("a caller without deployment:restore: %v", err)
	}
	w.auth.set("alice")

	// A queued rehearsal has done nothing, so it can be cancelled. (One
	// that runs is not stopped part way: TestAnInterruptedRehearsal...)
	queued, err := w.s.RequestRehearsal(ctx, w.project, "prod", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := w.ops.Cancel(ctx, queued.ID); err != nil || got.State != operations.Cancelled {
		t.Fatalf("cancelling a queued rehearsal: %+v %v", got, err)
	}

	edits := operationReceipts(t, w.db, "target.edited", ids.Nil)
	if len(edits) == 0 || !strings.Contains(string(edits[0].Payload), "Anyone who controls this host can read it") {
		t.Fatalf("the mark's receipt does not keep its meaning: %+v", edits)
	}
	yes := true
	if _, err := w.s.Targets.EditSettings(ctx, "do-syd", deployment.TargetSettingsEdit{ExpectedRevision: &w.dataWorld.target.SettingsRevision, Rehearsals: &yes}, "admin"); !errors.Is(err, deployment.ErrTargetRehearsals) {
		t.Fatalf("a provider target kept for rehearsals: %v", err)
	}
	if err := w.s.RefusesLanding(ctx, w.project, w.target); err == nil {
		t.Fatal("an ordinary deployment was placed on the host kept for rehearsals")
	}

	// A second kept host: which one is not guessed.
	second, err := w.s.Targets.Put(ctx, &deployment.Target{Name: "rehearse-2", Kind: deployment.KindDockerHost, Origin: deployment.OriginAttached, Status: deployment.TargetReady,
		Address: map[string]any{"hostname": "rehearse-2.example", "user": "forge", "keyFrom": "SSH"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Targets.EditSettings(ctx, "rehearse-2", deployment.TargetSettingsEdit{ExpectedRevision: &second.SettingsRevision, Rehearsals: &yes}, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.RequestRehearsal(ctx, w.project, "prod", ids.Nil); err == nil || !strings.Contains(err.Error(), "several Docker hosts") {
		t.Fatalf("two kept hosts: %v", err)
	}
	no := false
	for _, name := range []string{"rehearse-1", "rehearse-2"} {
		cur, _ := w.s.Targets.ByName(ctx, name)
		if _, err := w.s.Targets.EditSettings(ctx, name, deployment.TargetSettingsEdit{ExpectedRevision: &cur.SettingsRevision, Rehearsals: &no}, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.s.RequestRehearsal(ctx, w.project, "prod", ids.Nil); !errors.Is(err, ErrNoRehearsalHost) {
		t.Fatalf("no kept host: %v", err)
	}
	prot, err := w.s.DeploymentProtection(ctx, w.project, "prod")
	if err != nil || prot.RehearsalNote != "never rehearsed: no rehearsal place" {
		t.Fatalf("protection without a place: %+v %v", prot, err)
	}
}

// An AppTown that stops in the middle of a rehearsal, after the copy was
// staged on the rehearsal host and the stack made, settles it unknown at
// start. The deployment's last rehearsal then reads unknown, never absent
// and never an older pass; the next daily sweep removes the staged
// copy and the rehearsal's containers, volumes and network, and marks it
// cleared. At no point is the copy on AppTown's own disk, where a
// project's script steps run.
//
// Kills: staging the copy on AppTown's disk; the settlement dropping the
// recorded place; the last rehearsal skipping an unknown one; a sweep that
// skips unknown rehearsals or forgets the staged copy or the volumes.
func TestAnInterruptedRehearsalReadsUnknownAndIsClearedByTheSweep(t *testing.T) {
	t.Parallel()
	for _, earlier := range []bool{false, true} {
		t.Run(map[bool]string{false: "first rehearsal", true: "after a pass"}[earlier], func(t *testing.T) {
			t.Parallel()
			w := newRehearsalWorld(t)
			ctx := context.Background()
			var passed *operations.Operation
			if earlier {
				if passed = w.rehearse(); !endedAs(passed, OperationVerified) {
					t.Fatalf("the earlier rehearsal %s: %s", passed.State, passed.Detail)
				}
			}
			reached, release := make(chan struct{}), make(chan struct{})
			w.host.mu.Lock()
			w.host.beforeImport = func() { close(reached); <-release }
			w.host.mu.Unlock()
			op, err := w.s.RequestRehearsal(ctx, w.project, "prod", ids.Nil)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { _, _ = w.ops.Run(context.Background(), op.ID); close(done) }()
			<-reached
			// A rehearsal is not stopped part way: it takes itself down
			// when it ends.
			if _, err := w.ops.Cancel(ctx, op.ID); !errors.Is(err, ErrRehearsalNotStopped) {
				t.Errorf("cancelling a running rehearsal: %v", err)
			}
			place := placeOf(op.ID, "rehearse-1")
			w.host.mu.Lock()
			staged := w.host.volumes[place.Copy+"/postgres.dump"]
			w.host.mu.Unlock()
			if staged != "PGCOPY\norders: 41, 42" {
				t.Fatalf("the copy was not staged on the host before the stop: %q", staged)
			}
			_ = filepath.WalkDir(w.s.dataDir, func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				if body, _ := os.ReadFile(path); bytes.Contains(body, []byte("orders: 41, 42")) {
					t.Errorf("the copy is on AppTown's disk at %s", path)
				}
				return nil
			})
			// AppTown starts again.
			if r, err := w.restart(); err != nil || r.Settled != 1 {
				t.Fatalf("settled %+v: %v", r, err)
			}
			settled, err := w.ops.ByID(ctx, op.ID)
			if err != nil || settled.State != operations.Unknown {
				t.Fatalf("settled: %+v %v", settled, err)
			}
			if recorded, err := w.s.recordedPlace(ctx, op.ID); err != nil || recorded != place {
				t.Fatalf("the recorded place: %+v %v", recorded, err)
			}
			last, err := w.s.LastRehearsal(ctx, w.project, "prod")
			if err != nil || last == nil || last.Outcome != RehearsalUnknown || last.Passed || last.Operation != op.ID {
				t.Fatalf("the last rehearsal after the stop: %+v %v", last, err)
			}
			if earlier && (last.Previous == nil || last.Previous.Operation != passed.ID || !last.Previous.Passed) {
				t.Fatalf("the earlier pass is not kept as history: %+v", last.Previous)
			}
			cleared, left, err := w.s.SweepRehearsals(ctx)
			if err != nil || cleared != 1 || len(left) != 0 {
				t.Fatalf("sweep: cleared %d, left %v, %v", cleared, left, err)
			}
			cmds := fmt.Sprint(w.host.Commands())
			for _, want := range []string{"rm -f -v " + place.App, "volume rm " + docker.Current.DataVolume(place.App), "network rm " + docker.Current.Network(place.Stack), "volume rm " + place.Copy} {
				if !strings.Contains(cmds, want) {
					t.Errorf("the sweep did not remove %q", want)
				}
			}
			if again, _, _ := w.s.SweepRehearsals(ctx); again != 0 {
				t.Errorf("a cleared rehearsal was swept again")
			}
			close(release)
			<-done
		})
	}
}

// A restore rehearsal runs on its own host from a stored copy: losing the
// source target's copy runner neither stops it nor is said to (review
// CR-02's reproduction).
//
// Kills: gating the rehearsal on the source's copy runner.
func TestARehearsalNeedsNoCopyRunnerOnTheSource(t *testing.T) {
	t.Parallel()
	w := newRehearsalWorld(t)
	w.nameRunner("")
	p, err := w.s.DeploymentProtection(context.Background(), w.project, "prod")
	if err != nil || !p.Operations.Rehearsal.Available || p.Operations.Copy.Available {
		t.Fatalf("without a source runner: %+v %v", p.Operations, err)
	}
	if op := w.rehearse(); !endedAs(op, OperationVerified) {
		t.Fatalf("rehearsal %s: %s", op.State, op.Detail)
	}
}
