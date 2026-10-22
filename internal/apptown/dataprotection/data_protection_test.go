package dataprotection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/deploy/digitalocean/dofake"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/deploy/s3bucket/s3fake"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/oci"
	"github.com/flokkit/apptown/stock"
)

// grants is an authority that allows everything but the capabilities it
// denies, each in one project ("deployment:restore@12") or in all
// ("deployment:restore"); it remembers what it was asked.
type grants struct {
	mu   sync.Mutex
	who  string
	deny map[string]bool
	seen []access.Permission
	// via is how the caller came ("session" for a browser), "" for none.
	via string
	// revoked are credentials withdrawn since they asked (token:alice).
	revoked map[string]bool
}

// RequireStill is the check an operation asks before each effect: the
// credential that asked is not revoked, and current grants allow it.
func (g *grants) RequireStill(ctx context.Context, captured access.Principal, p access.Permission) error {
	g.mu.Lock()
	gone := g.revoked[captured.Credential] || captured.Credential == ""
	g.mu.Unlock()
	if gone {
		return fault.New(fault.Forbidden, "the credential "+captured.Credential+" was revoked")
	}
	if strings.HasPrefix(captured.Credential, "system:") {
		return nil
	}
	_, err := g.Require(ctx, p)
	return err
}

func (g *grants) revoke(credential string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.revoked == nil {
		g.revoked = map[string]bool{}
	}
	g.revoked[credential] = true
}

func (g *grants) Require(ctx context.Context, p access.Permission) (access.Caller, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.seen = append(g.seen, p)
	if g.deny[string(p.Capability)] || g.deny[string(p.Capability)+"@"+p.Resource.ProjectID.String()] {
		return access.Caller{}, fault.New(fault.Forbidden, fmt.Sprintf("%s may not %s in project %d", g.who, p.Capability, p.Resource.ProjectID))
	}
	who := g.who
	if c, ok := access.CallerFrom(ctx); ok && c.Principal.Via == "gate-ticket" {
		who = c.Principal.Name // AppTown asking again, as the person a ticket names
	}
	return access.Caller{Principal: access.Principal{Kind: "user", Name: who, Via: g.via, Credential: "token:" + who}, Verified: true}, nil
}

func (g *grants) set(who string, deny ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.who, g.deny = who, map[string]bool{}
	for _, d := range deny {
		g.deny[d] = true
	}
}

// fakeClusters is DigitalOcean's managed Postgres as a copy job meets it:
// each database's content by host and name. The copy commands are the stock
// kind's: an export writes the content, an import fills an existing, empty
// database, and verify passes only when the database holds the copy. Each
// job must be given the app user's password, by name only.
type fakeClusters struct {
	mu   sync.Mutex
	dbs  map[string]string // "<host>/<database>" -> content
	copy *dependency.Copy
	// failExport makes every export fail, as an unreachable cluster would.
	failExport bool
	// atFill is called when an import starts; atExport when an export does.
	atFill   func()
	atExport func()
	// leaked is a password seen in a job's arguments.
	leaked []string
	// importSays, when set, fails every import with it as the command's
	// output, as a copy breaking a constraint names the row.
	importSays string
}

func (f *fakeClusters) hook(op, cluster, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	host := cluster + ".fake"
	switch op {
	case "create":
		f.dbs[host+"/"+name] = ""
	case "drop":
		delete(f.dbs, host+"/"+name)
	case "delete-cluster":
		for k := range f.dbs {
			if strings.HasPrefix(k, host+"/") {
				delete(f.dbs, k)
			}
		}
	}
	return nil
}

func (f *fakeClusters) get(host, db string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.dbs[host+"/"+db]
	return v, ok
}

func (f *fakeClusters) put(host, db, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dbs[host+"/"+db] = content
}

func (f *fakeClusters) reply(c docker.Command) ([]byte, error, bool) {
	if len(c.Args) < 3 || c.Args[0] != "run" || c.Args[len(c.Args)-3] != "sh" {
		return nil, nil, false
	}
	command := c.Args[len(c.Args)-1]
	dep := map[string]string{}
	for _, kv := range c.Env {
		k, v, _ := strings.Cut(kv, "=")
		dep[k] = v
	}
	f.mu.Lock()
	for _, a := range c.Args {
		if strings.HasPrefix(a, "PGPASSWORD=") || strings.Contains(a, "pw-") {
			f.leaked = append(f.leaked, a)
		}
	}
	f.mu.Unlock()
	if dep["PGPASSWORD"] != "pw-"+dep["PGUSER"] || dep["PGSSLMODE"] != "require" {
		return []byte("password authentication failed"), errors.New("exit status 1"), true
	}
	key := dep["PGHOST"] + "/" + dep["PGDATABASE"]
	f.mu.Lock()
	defer f.mu.Unlock()
	content, exists := f.dbs[key]
	if !exists && dep["PGDATABASE"] == "defaultdb" {
		exists = true // every cluster has it, empty until someone writes to it
	}
	switch command {
	case f.copy.Export:
		if f.atExport != nil {
			f.mu.Unlock()
			f.atExport()
			f.mu.Lock()
		}
		if f.failExport || !exists {
			return []byte("could not connect"), errors.New("exit status 1"), true
		}
		_, _ = io.WriteString(c.Stdout, "PGCOPY\n"+content)
	case f.copy.Import:
		data, _ := io.ReadAll(c.Stdin)
		if f.importSays != "" {
			return []byte(f.importSays), errors.New("exit status 1"), true
		}
		if f.atFill != nil {
			f.mu.Unlock()
			f.atFill()
			f.mu.Lock()
		}
		switch {
		case !exists:
			return []byte("database does not exist"), errors.New("exit status 1"), true
		case content != "":
			return []byte("relation already exists"), errors.New("exit status 1"), true
		case !bytes.HasPrefix(data, []byte("PGCOPY\n")):
			return []byte("input file does not appear to be a valid archive"), errors.New("exit status 1"), true
		}
		f.dbs[key] = strings.TrimPrefix(string(data), "PGCOPY\n")
	case f.copy.Empty:
		if !exists || content != "" {
			return []byte("it holds 1 tables"), errors.New("exit status 1"), true
		}
	case f.copy.Verify:
		data, _ := io.ReadAll(c.Stdin)
		if !exists || "PGCOPY\n"+content != string(data) {
			return []byte("missing after import"), errors.New("exit status 1"), true
		}
	default:
		return nil, nil, false
	}
	return nil, nil, true
}

// dataWorld is a project, "ledger", whose contract needs postgres, with a
// bundle cut from it; a DigitalOcean account behind the App Platform target
// it offers; AppTown's own Docker host, named as that target's copy
// runner; and an S3-compatible store with a private bucket, attached as the
// backup store. Its managed Postgres is fakeClusters.
type dataWorld struct {
	integrations *integration.Service
	t            *testing.T
	s            *both
	db           *database.DB
	api          *dofake.API
	store        *s3fake.Store
	clusters     *fakeClusters
	buckets      *fakeBuckets
	auth         *grants
	ops          *operations.Service
	// onRequest, when set, is told of each request to the provider's API
	// before it is served ("POST /v2/databases").
	mu        sync.Mutex
	onRequest func(string)
	project   ids.ID
	apiBase   string
	bundle    *deployment.ReleaseBundle
	target    *deployment.Target
	contract  []byte
	// during runs once, in the middle of the next release request.
	during func()
}

// dataWorldOptions replace the fakes of a dataWorld with real things: a
// Docker client for the copy runner and the provider's hand-over of a new
// database to its user.
type dataWorldOptions struct {
	docker      docker.Client
	ownDatabase func(context.Context, string, string, string) error
	// contract replaces the app's .apptown/app.yaml.
	contract []byte
	// opsNow is the operations ledger's clock; nil is the real one.
	opsNow func() time.Time
}

// ledgerTemplates are the ledger repositories dataWorlds copy, one per
// contract: .apptown/app.yaml and a Dockerfile in one commit on main.
var ledgerTemplates sync.Map // contract -> *ledgerTemplate

type ledgerTemplate struct {
	*gittest.Template
	revision string
}

// ledgerRepository makes repo the test's own copy of the ledger repository
// with contract, and returns its commit.
func ledgerRepository(t *testing.T, repo string, contract []byte) string {
	t.Helper()
	fresh := &ledgerTemplate{}
	fresh.Template = gittest.NewTemplate(func(t testing.TB, dir string) {
		fresh.revision = gittest.Repository(t, dir, "main", gittest.Commit{Message: "ledger",
			Files: map[string]string{".apptown/app.yaml": string(contract), "Dockerfile": "FROM scratch\n"}})[0]
	})
	got, _ := ledgerTemplates.LoadOrStore(string(contract), fresh)
	tp := got.(*ledgerTemplate)
	tp.CopyTo(t, repo)
	return tp.revision
}

func newDataWorld(t *testing.T) *dataWorld { return newDataWorldWith(t, dataWorldOptions{}) }

func newDataWorldWith(t *testing.T, opts dataWorldOptions) *dataWorld {
	t.Helper()
	db := testDB(t)
	if err := jobs.Migrate(context.Background(), db.Pool()); err != nil {
		t.Fatal(err)
	}
	w := &dataWorld{t: t, db: db, api: &dofake.API{}, auth: &grants{who: "alice", deny: map[string]bool{}}}
	api := w.api.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		hook := w.onRequest
		w.mu.Unlock()
		if hook != nil {
			hook(r.Method + " " + r.URL.Path)
		}
		api.ServeHTTP(rw, r)
	}))
	t.Cleanup(srv.Close)
	w.store = s3fake.New(t, "STOREKEY")
	w.store.AddBucket("forge-copies")
	w.project = createTestProject(t, db, "ledger", "Ledger")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("DIGITALOCEAN_ACCESS_TOKEN=dop_v1_test\nSTORE_KEY=STOREKEY\nSTORE_SECRET=storesecret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "workspaces", "project-ledger")
	w.contract = []byte("app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  postgres:\n    version: \"16\"\n")
	if opts.contract != nil {
		w.contract = opts.contract
	}
	revision := ledgerRepository(t, repo, w.contract)
	projects := project.New(db, nil)
	if err := projects.SetDefaultBranch(context.Background(), w.project, "main"); err != nil {
		t.Fatal(err)
	}
	secrets := secret.NewService(db, secret.Config{DataDir: dir})
	catalog := dependency.NewCatalog(db, nil)
	if _, _, err := catalog.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	integrations := integration.NewService(db, integration.Dependencies{Projects: projects, Secrets: secrets, Kinds: catalog})
	w.integrations = integrations
	pg, err := catalog.ByCapability(t.Context(), "postgres")
	if err != nil {
		t.Fatal(err)
	}
	w.clusters = &fakeClusters{dbs: map[string]string{}, copy: pg.Copy}
	w.api.OnDatabase(w.clusters.hook)
	s3kind, err := catalog.ByCapability(t.Context(), "s3")
	if err != nil {
		t.Fatal(err)
	}
	w.buckets = newFakeBuckets(w.store, func() []map[string]any { return w.api.Snapshot().SpacesKeys }, s3kind.Copy)
	var client docker.Client = opts.docker
	if client == nil {
		daemon := dockerfake.New()
		daemon.SetCommandReply(func(c docker.Command) ([]byte, error, bool) {
			if out, err, ok := w.buckets.reply(c); ok {
				return out, err, true
			}
			return w.clusters.reply(c)
		})
		client = daemon
	}
	own := opts.ownDatabase
	if own == nil {
		own = func(context.Context, string, string, string) error { return nil }
	}
	sources := dataSources{repo: repo, yaml: string(w.contract), during: &w.during}
	w.apiBase = srv.URL
	// The account's Spaces are the same store: an app's bucket is made
	// there, and the keys the API makes reach it.
	w.api.ServeSpaces(w.store)
	options := digitalocean.Options{APIBase: srv.URL, PollInterval: time.Millisecond, DBPoll: time.Millisecond,
		SpacesEndpoint: func(string) string { return w.store.API.URL }, BucketSuffix: func() string { return "b0b0b0b0" },
		ProbeFrontDoor: func(context.Context, string, time.Time, func(string, ...any)) error { return nil },
		OwnDatabase:    own,
		PushImage: func(*digitalocean.Driver, context.Context, *digitalocean.Client, string, string, string, string, func(string, ...any)) (string, error) {
			return "sha256:" + strings.Repeat("a", 64), nil
		},
		// An image by digest (the gate's) keeps its digest in DOCR.
		CopyImage: func(_ *digitalocean.Driver, _ context.Context, _ *digitalocean.Client, _, _ string, from oci.Source, _ string, _ func(string, ...any)) (string, error) {
			_, digest, _ := strings.Cut(from.Reference, "@")
			return digest, nil
		}}
	w.ops = operations.New(db, w.auth, w.auth, opts.opsNow)
	w.s = assembled(db, testRuntime{Scope: docker.Scope{Client: client}, DataDir: dir, Projects: projects, Secrets: secrets, Integrations: integrations, Needs: catalog, ChownImage: "test-helper", BuiltInTarget: "local", DigitalOcean: options}, deployment.Dependencies{Operations: w.ops, Authority: w.auth, Followups: recoveryJobFollowups{}, Configuration: deployment.ConfigurationDependencies{Authority: w.auth, Projects: sources, Contracts: sources}, Releases: deployment.ReleaseDependencies{Authority: w.auth, Sources: sources, Secrets: doReleaseSecrets{}, Queue: doReleaseQueue{}}})
	if _, err := w.s.Targets.Put(context.Background(), &deployment.Target{Name: "local", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady}); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]any{"tokenFrom": "DIGITALOCEAN_ACCESS_TOKEN", "region": "syd", "registry": "forge-test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := integrations.Bindings.Save(context.Background(), "do-syd", digitalocean.CapCloudAccount, "digitalocean", string(config), "fixture"); err != nil {
		t.Fatal(err)
	}
	w.s.SeedCloudTargets(t.Context(), "fixture")
	if w.target, err = w.s.Targets.ByName(context.Background(), "do-syd"); err != nil {
		t.Fatal(err)
	}
	if err := w.s.Targets.Offer(context.Background(), w.target, "*", deployment.OfferCaps{}, "admin"); err != nil {
		t.Fatal(err)
	}
	bid, err := w.s.Bundles.Cut(t.Context(), w.project, revision, map[string]string{"web": "sha256:web"}, nil, w.contract, deployment.BundleOrigin{})
	if err != nil {
		t.Fatal(err)
	}
	if w.bundle, err = w.s.Bundles.ByID(context.Background(), bid); err != nil {
		t.Fatal(err)
	}
	return w
}

// protect attaches the backup store and names the copy runner.
func (w *dataWorld) protect() {
	w.t.Helper()
	if _, err := w.s.AttachStore(context.Background(), AttachStore{Endpoint: w.store.API.URL, Region: "syd1", Bucket: "forge-copies", AccessKeyFrom: "STORE_KEY", SecretKeyFrom: "STORE_SECRET"}); err != nil {
		w.t.Fatal(err)
	}
	w.nameRunner("local")
}

// nameRunner names the target's copy runner, or none when name is "".
func (w *dataWorld) nameRunner(name string) {
	w.t.Helper()
	t, err := w.s.Targets.EditSettings(context.Background(), "do-syd", deployment.TargetSettingsEdit{ExpectedRevision: &w.target.SettingsRevision, CopyRunner: &name}, "admin")
	if err != nil {
		w.t.Fatal(err)
	}
	w.target = t
}

// bind binds a deployment on the App Platform target, starting from a set
// when from is not 0.
func (w *dataWorld) bind(project ids.ID, name, data string, from ids.ID) (*deployment.Deployment, error) {
	res, err := w.s.Configure(context.Background(), deployment.ConfigureDeployment{ProjectID: project, Name: name, Target: "do-syd", Attributes: deployment.Attributes{Feed: "promotion", Audience: "team", DataClass: data}, StartFrom: from})
	if err != nil {
		return nil, err
	}
	return res.Deployment, nil
}

// release asks for the bundle's release to name and runs it, as its job
// does, answering the release and its log.
func (w *dataWorld) release(name string) (*deployment.Release, string) {
	w.t.Helper()
	return w.releaseAs(deployment.DeploySelection{ProjectID: w.project, DeploymentName: name, BundleID: w.bundle.ID})
}

// releaseAs requests the release in, runs it and reads back its log.
func (w *dataWorld) releaseAs(in deployment.DeploySelection) (*deployment.Release, string) {
	w.t.Helper()
	accepted, err := w.s.RequestRelease(context.Background(), in)
	if err != nil {
		w.t.Fatal(err)
	}
	_ = w.s.ExecuteRelease(context.Background(), accepted.ID)
	rel, err := w.s.Releases.ByNumber(context.Background(), w.project, accepted.Number)
	if err != nil {
		w.t.Fatal(err)
	}
	rows, err := w.s.Releases.Events(context.Background(), rel.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	var log []string
	for _, r := range rows {
		log = append(log, r.Text)
	}
	return rel, strings.Join(log, "\n")
}

// run runs an admitted operation as its job does and reads it back.
func (w *dataWorld) run(op *operations.Operation) *operations.Operation {
	w.t.Helper()
	if _, err := w.ops.Run(context.Background(), op.ID); err != nil {
		w.t.Fatal(err)
	}
	got, err := w.ops.ByID(context.Background(), op.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return got
}

// restart runs the operations ledger's restart repair, as an exclusive
// start does.
func (w *dataWorld) restart() (operations.RestartRepair, error) {
	return database.Transact(context.Background(), w.db, func(tx *database.Tx) (operations.RestartRepair, error) {
		return w.ops.RepairInterruptedIn(context.Background(), tx)
	})
}

// opsOn is the operations on the deployment named, newest first.
func (w *dataWorld) opsOn(name string) ([]operations.Operation, error) {
	dep, err := w.s.Deployments.ByName(context.Background(), w.project, name)
	if err != nil {
		return nil, err
	}
	return w.ops.OnSubject(context.Background(), deployment.SubjectOf(dep), 5)
}

// endedAs says an operation ended as a kind's body said: verified and not
// verified are success with that verification, done is success without.
func endedAs(op *operations.Operation, end runEnd) bool {
	switch end {
	case OperationVerified:
		return op.State == operations.Succeeded && op.Verification == operations.Verified
	case OperationNotVerified:
		return op.State == operations.Succeeded && op.Verification == operations.NotVerified
	case OperationDone:
		return op.State == operations.Succeeded && op.Verification == ""
	}
	return op.State == operations.Failed
}

// clusterOf is the managed instance a deployment's postgres is recorded on.
func (w *dataWorld) clusterOf(dep *deployment.Deployment) map[string]any {
	w.t.Helper()
	insts, err := w.s.Needs.ForDeployment(context.Background(), dep.ID)
	if err != nil || len(insts) != 1 {
		w.t.Fatalf("need instances: %v %v", insts, err)
	}
	return insts[0].Handle
}

type dataSources struct {
	repo, yaml string
	// during, when set, runs while a release request resolves its source:
	// another person acting in the middle of that request.
	during *func()
}

func (s dataSources) Project(_ context.Context, id ids.ID) (deployment.ReleaseProject, error) {
	return deployment.ReleaseProject{Application: deployment.Application{ID: id, Slug: "ledger"}, Repository: s.repo}, nil
}
func (s dataSources) Contract(_ context.Context, _ deployment.ReleaseProject, revision string) (*appcontract.Contract, string, error) {
	if s.during != nil && *s.during != nil {
		f := *s.during
		*s.during = nil
		f()
	}
	c, err := appcontract.Parse([]byte(s.yaml))
	return c, revision, err
}
func (s dataSources) CurrentContract(context.Context, string) (*appcontract.Contract, error) {
	return appcontract.Parse([]byte(s.yaml))
}

func operationReceipts(t *testing.T, db *database.DB, kind string, project ids.ID) []audit.Entry {
	t.Helper()
	rows, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: kind, ProjectID: project, Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// The round trip (design/subsystems/deployment.md, "Protecting data
// off-host"):
// a production App Platform deployment holding records is taken down — the
// app is deleted first, a final copy goes to the store, is filled into a
// new database on the same cluster and checked, and only then is the
// cluster deleted — and a new deployment of the same name starts from that
// copy: its first release fills its new database before its app is made,
// and the records are there. The receipts name the app and release, the
// set and its digest, and the old and new cluster and database.
//
// Kills: deleting the cluster before the copy is checked; filling after the
// app is made; adopting the old database; a copy written public; the
// password in a job's arguments; a final copy that is not recorded or
// verified; a start not recorded on the deployment; the old deployment's
// drill or operations shown as the new one's.
func TestATakenDownDeploymentComesBackFromItsFinalCopy(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	w.protect()
	ctx := context.Background()
	dep, err := w.bind(w.project, "prod", "production", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if rel, log := w.release("prod"); rel.State != deployment.Released {
		t.Fatalf("first release %s:\n%s", rel.State, log)
	}
	old := w.clusterOf(dep)
	oldID, _ := old["id"].(string)
	// The app writes its records.
	w.clusters.put(oldID+".fake", "app", "ledger: 41, 42")

	res, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"})
	if err != nil || res.Operation == nil || res.Removed != nil {
		t.Fatalf("take-down door: %+v %v", res, err)
	}
	op := w.run(res.Operation)
	if !endedAs(op, OperationVerified) {
		t.Fatalf("take-down %s: %s\n%+v", op.State, op.Detail, op.Steps)
	}
	// The order: the app went before the copy, and the cluster only after
	// the copy came back.
	var names []string
	for _, st := range op.Steps {
		if st.Failed {
			t.Fatalf("a failed step in a take-down that succeeded: %+v", st)
		}
		names = append(names, st.Name)
	}
	order := strings.Join(names, " | ")
	app, copied, checked, cluster := strings.Index(order, "deleting the app"), strings.Index(order, "copying postgres"), strings.Index(order, "filling a new database"), strings.Index(order, "deleting the cluster")
	if app < 0 || copied < app || checked < copied || cluster < checked {
		t.Fatalf("steps out of order: %s", order)
	}
	// The record says which other databases were shown empty before the
	// cluster went, how and as whom: the provider's defaultdb here.
	const shownEmpty = "defaultdb shown empty (the postgres kind's empty check, on "
	if last := op.Steps[len(op.Steps)-2]; !strings.HasPrefix(last.Name, "deleting the cluster") || !strings.Contains(last.Detail, shownEmpty) || !strings.Contains(last.Detail, " as app)") {
		t.Fatalf("the cluster's deletion step: %+v", last)
	}
	state := w.api.Snapshot()
	if len(state.Apps) != 0 || len(state.DeletedClusters) != 1 || state.DeletedClusters[0] != oldID {
		t.Fatalf("after take-down: apps %d, deleted clusters %v", len(state.Apps), state.DeletedClusters)
	}
	if _, err := w.s.Deployments.ByName(ctx, w.project, "prod"); err == nil {
		t.Fatal("the deployment's record stands after its take-down")
	}
	if open, err := w.s.Retained.ForProject(ctx, w.project); err != nil || len(open) != 0 {
		t.Fatalf("a take-down that deleted the cluster after a checked copy left %v (%v)", open, err)
	}
	finals, err := w.s.FinalCopies(ctx, w.project)
	if err != nil || len(finals) != 1 || !finals[0].Verified || finals[0].Deployment != "prod" || finals[0].DataClass != "production" {
		t.Fatalf("final copies: %+v %v", finals, err)
	}
	set, err := w.s.backups.ByID(ctx, w.project, "prod", finals[0].Backup)
	if err != nil {
		t.Fatal(err)
	}
	item := set.GoodCopy("need", "postgres")
	if set.Store != StoreBackupStore || item == nil || item.Instance != oldID || item.Database != "app" || item.Format != "pg-custom" || item.AsUser != "app" {
		t.Fatalf("final set: %+v", set)
	}
	obj, ok := w.store.Get("forge-copies", set.Path+"/"+item.File)
	if !ok || obj.ACL != "private" || obj.Public || string(obj.Body) != "PGCOPY\nledger: 41, 42" || obj.SHA256 != item.SHA256 {
		t.Fatalf("the copy in the store: %+v %v", obj, ok)
	}
	// The manifest names its set, so the bucket alone ties copy to set.
	m, ok := w.store.Get("forge-copies", set.Path+"/manifest.json")
	if !ok {
		t.Fatal("no manifest beside the copy")
	}
	var named struct {
		ID ids.ID `json:"id"`
	}
	if err := json.Unmarshal(m.Body, &named); err != nil || named.ID != set.ID {
		t.Fatalf("the manifest names set %d, not %d (%v)", named.ID, set.ID, err)
	}
	torn := operationReceipts(t, w.db, "deployment.torn-down", w.project)
	if len(torn) != 1 || !strings.Contains(string(torn[0].Payload), oldID) || !strings.Contains(string(torn[0].Payload), fmt.Sprint(set.ID)) || !strings.Contains(string(torn[0].Payload), shownEmpty) {
		t.Fatalf("take-down receipt: %+v", torn)
	}

	// Deploy again from the final copy, under the same name.
	var appsAtFill = -1
	w.clusters.atFill = func() { appsAtFill = len(w.api.Snapshot().Apps) }
	again, err := w.bind(w.project, "prod", "production", set.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.StartFrom != set.ID || again.StartedFrom.Valid {
		t.Fatalf("the start before its release: %+v", again)
	}
	rel, log := w.release("prod")
	if rel.State != deployment.Released {
		t.Fatalf("release from the copy %s:\n%s", rel.State, log)
	}
	if appsAtFill != 0 {
		t.Fatalf("the app existed (%d) when its database was filled", appsAtFill)
	}
	if !strings.Contains(log, fmt.Sprintf("starting from set %d", set.ID)) || !strings.Contains(log, "filling the new database app on apptown-ledger-prod from it") || !strings.Contains(log, "postgres came back") {
		t.Fatalf("release log:\n%s", log)
	}
	now, err := w.s.Deployments.ByName(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	newer := w.clusterOf(now)
	newID, _ := newer["id"].(string)
	if newID == oldID || !now.StartedFrom.Valid || now.StartFrom != set.ID {
		t.Fatalf("the new deployment: cluster %s (old %s), %+v", newID, oldID, now)
	}
	if got, _ := w.clusters.get(newID+".fake", "app"); got != "ledger: 41, 42" {
		t.Fatalf("the records through the new database: %q", got)
	}
	// The new deployment's protection is its own: the old one's drill and
	// take-down, under the same name, are not shown as this one's.
	prot, err := w.s.DeploymentProtection(ctx, w.project, "prod")
	if err != nil || len(prot.Items) != 1 || prot.Items[0].Drill != nil || len(prot.Recent) != 0 || prot.StartFrom == nil || prot.StartFrom.Backup != set.ID {
		t.Fatalf("the new deployment's protection: %+v %v", prot, err)
	}
	started := operationReceipts(t, w.db, "deployment.started-from-copy", w.project)
	detail := string(started[0].Payload)
	if len(started) != 1 || !strings.Contains(detail, oldID) || !strings.Contains(detail, newID) || !strings.Contains(detail, item.SHA256) || !strings.Contains(detail, fmt.Sprintf(`"release":%d`, rel.Number)) {
		t.Fatalf("start receipt: %s", detail)
	}
	// A later release does not fill it again.
	w.clusters.put(newID+".fake", "app", "ledger: 41, 42, 43")
	if rel, log := w.release("prod"); rel.State != deployment.Released || strings.Contains(log, "starting from set") {
		t.Fatalf("a later release filled again: %s\n%s", rel.State, log)
	}
	if got, _ := w.clusters.get(newID+".fake", "app"); got != "ledger: 41, 42, 43" {
		t.Fatalf("a later release changed the data: %q", got)
	}
	if len(w.clusters.leaked) != 0 {
		t.Fatalf("a password in a job's arguments: %v", w.clusters.leaked)
	}
}

// A release whose parts are different images is refused for App Platform
// when it is asked for, before any effect at DigitalOcean: App Platform
// runs one version of a repository per app and refuses such an app only at
// create, so a later refusal would leave its managed database (and an app
// with no deployment) behind. The refusal names the remedy AppTown supports.
// The same app with one image is released.
//
// Kills: the refusal left to the renderer or the platform, after the
// release's database was made.
func TestAMultiImageReleaseToAppPlatformMakesNoDatabase(t *testing.T) {
	t.Parallel()
	contract := []byte("app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n  worker:\n    build: worker\nneeds:\n  postgres:\n    version: \"16\"\n")
	w := newDataWorldWith(t, dataWorldOptions{contract: contract})
	if _, err := w.bind(w.project, "cloud", "synthetic", ids.Nil); err != nil {
		t.Fatal(err)
	}
	cut := func(parts map[string]string) ids.ID {
		t.Helper()
		bid, err := w.s.Bundles.Cut(t.Context(), w.project, w.bundle.ContractRevision, parts, nil, w.contract, deployment.BundleOrigin{})
		if err != nil {
			t.Fatal(err)
		}
		return bid
	}
	before := len(w.api.Snapshot().Requests)
	_, err := w.s.RequestRelease(context.Background(), deployment.DeploySelection{ProjectID: w.project, DeploymentName: "cloud", BundleID: cut(map[string]string{"web": "sha256:web", "worker": "sha256:worker"})})
	if !errors.Is(err, deployment.ErrReleaseRefused) || !strings.HasSuffix(err.Error(), "App Platform runs one image per repository, and this release's parts and init steps use 2 different images in the project's one repository. Deploy it to a Docker host target instead.") {
		t.Fatalf("a release of two images to App Platform: %v", err)
	}
	state := w.api.Snapshot()
	if state.ClustersMade != 0 || len(state.Databases) != 0 || len(state.Apps) != 0 || len(state.Requests) != before {
		t.Fatalf("the refusal reached DigitalOcean: %d clusters, %d apps, requests %v", state.ClustersMade, len(state.Apps), state.Requests[before:])
	}
	if _, err := w.s.RequestRelease(context.Background(), deployment.DeploySelection{ProjectID: w.project, DeploymentName: "cloud", BundleID: cut(map[string]string{"web": "sha256:app", "worker": "sha256:app"})}); err != nil {
		t.Fatalf("a release of one image was refused: %v", err)
	}
}

// cloudClient is the provider's API for the world's target, with the
// connector's credential, as the target's drivers reach it.
func (w *dataWorld) cloudClient(ctx context.Context) (*digitalocean.Client, error) {
	cfg, err := w.s.providers.DigitalOceanConfig(ctx, w.target.Provider)
	if err != nil {
		return nil, err
	}
	return w.s.providers.DigitalOceanClient(ctx, cfg)
}
