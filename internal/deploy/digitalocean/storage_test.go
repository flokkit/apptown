package digitalocean

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/digitalocean/dofake"
	"github.com/flokkit/apptown/internal/deploy/s3bucket"
	"github.com/flokkit/apptown/internal/deploy/s3bucket/s3fake"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/stock"
)

// memHoldings is a deployment's owned list in memory.
type memHoldings struct {
	mu    sync.Mutex
	owned []deploy.Owned
}

func (h *memHoldings) Owned(context.Context) ([]deploy.Owned, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.owned), nil
}
func (h *memHoldings) Created(_ context.Context, o deploy.Owned) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.owned = append(h.owned, o)
	return nil
}
func (h *memHoldings) Forget(_ context.Context, kind, id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.owned = slices.DeleteFunc(h.owned, func(o deploy.Owned) bool { return o.Kind == kind && o.ID == id })
	return nil
}

// memSecrets is the deployment's secret scope in memory.
type memSecrets map[string]string

func (s memSecrets) Get(_ context.Context, name string) (string, error) { return s[name], nil }
func (s memSecrets) Put(_ context.Context, name, value string) error    { s[name] = value; return nil }
func (s memSecrets) Delete(_ context.Context, name string) error        { delete(s, name); return nil }

// spacesAccount is a DigitalOcean account with Spaces: the API and the
// store its keys reach.
type spacesAccount struct {
	api      *dofake.API
	store    *s3fake.Store
	holdings *memHoldings
	secrets  memSecrets
	driver   *Driver
	log      []string
}

func newSpacesAccount(t *testing.T) *spacesAccount {
	t.Helper()
	a := &spacesAccount{api: &dofake.API{}, store: s3fake.New(t, ""), holdings: &memHoldings{}, secrets: memSecrets{}}
	a.api.ServeSpaces(a.store)
	server := httptest.NewServer(a.api.Handler())
	t.Cleanup(server.Close)
	client := NewClient("test", server.URL)
	a.driver = NewDriver(Dependencies{
		Client:   func(context.Context) (*Client, Config, error) { return client, Config{Region: "syd"}, nil },
		Slug:     "shop",
		Holdings: a.holdings,
		Secrets:  a.secrets,
	}, Options{SpacesEndpoint: func(string) string { return a.store.API.URL }, BucketSuffix: func() string { return "abcd1234" },
		PollInterval: time.Millisecond, GrantWait: time.Second})
	return a
}

func (a *spacesAccount) logf(format string, args ...any) {
	a.log = append(a.log, fmt.Sprintf(format, args...))
}

func (a *spacesAccount) fulfil(t *testing.T) (deploy.NeedBinding, map[string]any, error) {
	t.Helper()
	return a.driver.FulfilNeed(context.Background(), shopMain, "s3", nil, a.logf)
}

func (a *spacesAccount) keyNames() []string {
	var out []string
	for _, k := range a.api.Snapshot().SpacesKeys {
		out = append(out, k["name"].(string))
	}
	return out
}

// stockProvides is the s3 need's contract as the catalogue states it.
func stockProvides(t *testing.T) []string {
	t.Helper()
	raw, err := stock.FS.ReadFile("dependencies/s3.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var kind struct {
		Provides map[string]string `yaml:"provides"`
	}
	if err := yaml.Unmarshal(raw, &kind); err != nil {
		t.Fatal(err)
	}
	var names []string
	for n := range kind.Provides {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// On App Platform the s3 need is the deployment's own Spaces bucket and a
// key for it alone: the release makes one of each, records both on the
// deployment's owned list, keeps the key's secret, deletes the
// full-access key it made the bucket with, and gives the app exactly the
// contract's variables (the secret as a secret). The key puts and reads
// objects and cannot make a bucket. A second release makes nothing.
func TestAnS3NeedOnAppPlatformIsABucketAndAKeyForItAlone(t *testing.T) {
	t.Parallel()
	a := newSpacesAccount(t)
	b, handle, err := a.fulfil(t)
	if err != nil {
		t.Fatal(err)
	}
	if handle != nil || b.Native != nil {
		t.Fatalf("the bucket is not a managed need instance: %v %v", handle, b.Native)
	}
	var names []string
	for n := range b.Env {
		names = append(names, n)
	}
	for n := range b.Secret {
		names = append(names, n)
	}
	slices.Sort(names)
	if want := stockProvides(t); !slices.Equal(names, want) {
		t.Fatalf("the binding gives %v, the contract provides %v", names, want)
	}
	bucket := "apptown-3b89d1da8075-abcd1234"
	if b.Env["S3_BUCKET"] != bucket || b.Env["AWS_REGION"] != "syd1" || b.Env["AWS_ENDPOINT_URL_S3"] != a.store.API.URL || b.Secret["AWS_SECRET_ACCESS_KEY"] == "" {
		t.Fatalf("the binding: %+v", b)
	}
	if got := a.store.Buckets(); !slices.Equal(got, []string{bucket}) {
		t.Fatalf("buckets %v", got)
	}
	if got := a.keyNames(); !slices.Equal(got, []string{"apptown-app-3b89d1da8075"}) {
		t.Fatalf("the account's keys after the release: %v (the maker key must be gone)", got)
	}
	owned, _ := a.holdings.Owned(context.Background())
	want := []deploy.Owned{
		{Kind: KindBucket, ID: bucket, Name: bucket, Data: true, Need: "s3"},
		{Kind: KindSpacesKey, ID: b.Env["AWS_ACCESS_KEY_ID"], Name: "apptown-app-3b89d1da8075", Need: "s3"},
	}
	if !slices.Equal(owned, want) {
		t.Fatalf("owned %+v, want %+v", owned, want)
	}
	if a.secrets[SpacesSecretName] != b.Secret["AWS_SECRET_ACCESS_KEY"] {
		t.Fatal("the key's secret is not kept")
	}
	app := &s3bucket.Client{Endpoint: a.store.API.URL, Region: "syd1", AccessKey: b.Env["AWS_ACCESS_KEY_ID"], SecretKey: "x"}
	if err := app.PutObject(context.Background(), bucket, "notes/one.txt", "text/plain", false, []byte("kept")); err != nil {
		t.Fatalf("the app's key puts an object: %v", err)
	}
	if err := app.CreateBucket(context.Background(), "another"); !s3bucket.IsCode(err, "AccessDenied") {
		t.Fatalf("the app's key made a bucket: %v", err)
	}
	made := a.api.Snapshot().SpacesKeysMade
	again, _, err := a.fulfil(t)
	if err != nil || again.Env["S3_BUCKET"] != bucket || a.api.Snapshot().SpacesKeysMade != made || len(a.store.Buckets()) != 1 {
		t.Fatalf("a second release made something: %v %+v keys made %d", err, again, a.api.Snapshot().SpacesKeysMade)
	}
}

// The full-access maker key goes whatever happens: when the bucket cannot
// be made, and when an interrupted operation left one of its name, which is
// deleted before another is made. One whose deletion fails is said, named,
// in the record.
func TestTheMakerKeyGoesWhateverHappens(t *testing.T) {
	t.Parallel()
	t.Run("a bucket that cannot be made", func(t *testing.T) {
		t.Parallel()
		a := newSpacesAccount(t)
		a.store.Fail("PUT", "apptown-3b89d1da8075-abcd1234", "", 500)
		if _, _, err := a.fulfil(t); err == nil || !strings.Contains(err.Error(), "whether it was made is not known") {
			t.Fatalf("an unknown create: %v", err)
		}
		if got := a.keyNames(); len(got) != 0 {
			t.Fatalf("keys left: %v", got)
		}
	})
	t.Run("a name that is taken", func(t *testing.T) {
		t.Parallel()
		a := newSpacesAccount(t)
		a.store.AddBucket("apptown-3b89d1da8075-abcd1234")
		_, _, err := a.fulfil(t)
		if err == nil || !deploy.IsTerminal(err) || !strings.Contains(err.Error(), "never adopts") {
			t.Fatalf("a taken name: %v", err)
		}
		if owned, _ := a.holdings.Owned(context.Background()); len(owned) != 0 || len(a.keyNames()) != 0 {
			t.Fatalf("a taken bucket was adopted: %v, keys %v", owned, a.keyNames())
		}
	})
	t.Run("one an interrupted release left", func(t *testing.T) {
		t.Parallel()
		a := newSpacesAccount(t)
		a.store.AddBucket("elsewhere")
		left, err := a.driver.mintKey(context.Background(), mustClient(t, a), "apptown-maker-3b89d1da8075", spacesGrant{Permission: "fullaccess"})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := a.fulfil(t); err != nil {
			t.Fatal(err)
		}
		if got := a.keyNames(); !slices.Equal(got, []string{"apptown-app-3b89d1da8075"}) || !slices.ContainsFunc(a.log, func(l string) bool { return strings.Contains(l, left.AccessKey) }) {
			t.Fatalf("keys %v after a release; log %v", got, a.log)
		}
	})
	t.Run("one whose deletion fails", func(t *testing.T) {
		t.Parallel()
		a := newSpacesAccount(t)
		a.api.Fail("DELETE", "/v2/spaces/keys/DOFAKE1", 500)
		if _, _, err := a.fulfil(t); err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(a.log, func(l string) bool {
			return strings.Contains(l, "apptown-maker-3b89d1da8075 (DOFAKE1) was NOT deleted") && strings.Contains(l, "live credential")
		}) {
			t.Fatalf("the left maker key is not said: %v", a.log)
		}
	})
}

func mustClient(t *testing.T, a *spacesAccount) *Client {
	t.Helper()
	c, _, err := a.driver.Client(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A recorded key whose secret AppTown no longer holds cannot be given to
// the app: it is deleted, and another is made for the same bucket.
func TestAKeyWhoseSecretIsGoneIsReplaced(t *testing.T) {
	t.Parallel()
	a := newSpacesAccount(t)
	first, _, err := a.fulfil(t)
	if err != nil {
		t.Fatal(err)
	}
	delete(a.secrets, SpacesSecretName)
	second, _, err := a.fulfil(t)
	if err != nil {
		t.Fatal(err)
	}
	if second.Env["AWS_ACCESS_KEY_ID"] == first.Env["AWS_ACCESS_KEY_ID"] || second.Env["S3_BUCKET"] != first.Env["S3_BUCKET"] || second.Secret["AWS_SECRET_ACCESS_KEY"] == "" {
		t.Fatalf("first %+v second %+v", first, second)
	}
	owned, _ := a.holdings.Owned(context.Background())
	keys := slices.DeleteFunc(owned, func(o deploy.Owned) bool { return o.Kind != KindSpacesKey })
	if len(keys) != 1 || keys[0].ID != second.Env["AWS_ACCESS_KEY_ID"] || !slices.Equal(a.keyNames(), []string{"apptown-app-3b89d1da8075"}) {
		t.Fatalf("keys recorded %+v, at the account %v", keys, a.keyNames())
	}
}

// Taking an App Platform deployment down deletes its Spaces key, and its
// bucket with every object only when its data may go (synthetic); a bucket
// holding data no copy covers is kept, said so, and reported left as data.
func TestTakeDownDeletesTheKeyAndTheBucketOnlyWhenItsDataMayGo(t *testing.T) {
	t.Parallel()
	for _, synthetic := range []bool{true, false} {
		t.Run(fmt.Sprintf("synthetic %v", synthetic), func(t *testing.T) {
			t.Parallel()
			a := newSpacesAccount(t)
			b, _, err := a.fulfil(t)
			if err != nil {
				t.Fatal(err)
			}
			a.store.Put(b.Env["S3_BUCKET"], "notes/one.txt", s3fake.Object{Body: []byte("kept")})
			owned, _ := a.holdings.Owned(context.Background())
			taken, err := a.driver.Teardown(context.Background(), shopMain, 0, deploy.TakeDown{Owned: owned, Synthetic: synthetic})
			if err != nil {
				t.Fatal(err)
			}
			if len(a.keyNames()) != 0 || a.secrets[SpacesSecretName] != "" {
				t.Fatalf("the key outlived the take-down: %v %v", a.keyNames(), a.secrets)
			}
			buckets := a.store.Buckets()
			if synthetic {
				if len(buckets) != 0 || len(taken.Left) != 0 || !slices.ContainsFunc(taken.Deleted, func(d string) bool { return strings.Contains(d, "bucket "+b.Env["S3_BUCKET"]) }) {
					t.Fatalf("synthetic take-down: buckets %v, %+v", buckets, taken)
				}
				return
			}
			if !slices.Equal(buckets, []string{b.Env["S3_BUCKET"]}) || len(taken.Left) != 1 || !taken.Left[0].Data || !strings.Contains(taken.Left[0].Why, "no final copy of its objects") || taken.Left[0].Handle["name"] != b.Env["S3_BUCKET"] {
				t.Fatalf("a bucket holding data: buckets %v, %+v", buckets, taken)
			}
			if _, ok := a.store.Get(b.Env["S3_BUCKET"], "notes/one.txt"); !ok {
				t.Fatal("the kept bucket lost its object")
			}
		})
	}
}

// The app spec carries an object store's key secret to every part and
// init step as the platform's secret, and never in the receipt's hash.
func TestTheBucketKeysSecretIsRenderedAsASecret(t *testing.T) {
	t.Parallel()
	api := &dofake.API{}
	server := httptest.NewServer(api.Handler())
	defer server.Close()
	push := func(_ *Driver, _ context.Context, _ *Client, _, _, _, _ string, _ func(string, ...any)) (string, error) {
		return "sha256:" + strings.Repeat("a", 64), nil
	}
	driver := NewDriver(Dependencies{Slug: "shop", Client: func(context.Context) (*Client, Config, error) {
		return &Client{Base: server.URL, HTTP: server.Client()}, Config{Region: "syd", Registry: "forge-test"}, nil
	}}, Options{PushImage: push})
	c, err := appcontract.Parse([]byte("app:\n  web: {image: app:1, public: true}\n  worker: {image: app:1}\ninit:\n  - {name: migrate, image: app:1}\nneeds: {s3: {}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	b := deploy.NeedBinding{Env: map[string]string{"S3_BUCKET": "apptown-3b89d1da8075-abcd1234"}, Secret: map[string]string{"AWS_SECRET_ACCESS_KEY": "s3cr3t"}}
	art, err := driver.Render(context.Background(), shopMain, deploy.Source{Images: map[string]string{"web": "app:1", "worker": "app:1"}, Init: map[string]string{"migrate": "app:1"}, Contract: c, Revision: "r"},
		deploy.NeedBindings{"s3": b}, scopedRenderSecrets{}, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	var spec AppSpec
	if err := json.Unmarshal(renderedSpecBody(t, art), &spec); err != nil {
		t.Fatal(err)
	}
	for _, comp := range append(append(spec.Services, spec.Workers...), spec.Jobs...) {
		i := slices.IndexFunc(comp.Envs, func(e doEnv) bool { return e.Key == "AWS_SECRET_ACCESS_KEY" })
		if i < 0 || comp.Envs[i].Type != "SECRET" || comp.Envs[i].Value != "s3cr3t" {
			t.Fatalf("%s: the key secret %+v", comp.Name, comp.Envs)
		}
	}
	if strings.Contains(art.Hash, "s3cr3t") || strings.Contains(blankSecrets(spec), "s3cr3t") {
		t.Fatal("the secret reached the receipt's hash")
	}
}

// The account's inventory lists its Spaces keys and what each reaches. A
// key is reported only to confirm a record, except a maker key an AppTown
// left, which reads as unrecorded: a live full-access credential.
func TestTheInventoryListsSpacesKeysAndALeftMakerKey(t *testing.T) {
	t.Parallel()
	a := newSpacesAccount(t)
	if _, _, err := a.fulfil(t); err != nil {
		t.Fatal(err)
	}
	if _, err := a.driver.mintKey(context.Background(), mustClient(t, a), "apptown-maker-3b89d1da8075", spacesGrant{Permission: "fullaccess"}); err != nil {
		t.Fatal(err)
	}
	got, err := Inventory{Client: a.driver.Client}.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var app, maker *deploy.Resource
	for i := range got {
		switch got[i].Name {
		case "apptown-app-3b89d1da8075":
			app = &got[i]
		case "apptown-maker-3b89d1da8075":
			maker = &got[i]
		}
	}
	if app == nil || app.Kind != KindSpacesKey || !slices.Contains(app.Tags, deploy.TagConfirmsOnly) || app.Detail != "readwrite on the bucket apptown-3b89d1da8075-abcd1234" {
		t.Fatalf("the app's key: %+v", app)
	}
	if maker == nil || slices.Contains(maker.Tags, deploy.TagConfirmsOnly) || maker.Detail != "fullaccess on every bucket" {
		t.Fatalf("the left maker key: %+v", maker)
	}
}

// A copy of the bucket reaches it as the deployment's own key: the Spaces
// endpoint's host and port, TLS as its scheme says, the bucket, the key on
// the owned list and its kept secret. Only the deployment's own bucket is
// answered for, and a key whose secret is gone gives no connection.
func TestTheBucketsCopyIsGivenTheDeploymentsOwnKey(t *testing.T) {
	t.Parallel()
	a := newSpacesAccount(t)
	b, _, err := a.fulfil(t)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(a.store.API.URL)
	port, _ := strconv.Atoi(u.Port())
	conn, err := a.driver.BucketConnection(context.Background(), b.Env["S3_BUCKET"])
	want := deploy.Connection{Host: u.Hostname(), Port: port, Database: b.Env["S3_BUCKET"], User: b.Env["AWS_ACCESS_KEY_ID"], Password: b.Secret["AWS_SECRET_ACCESS_KEY"], SSLMode: "disable"}
	if err != nil || conn != want {
		t.Fatalf("the connection %+v %v, want %+v", conn, err, want)
	}
	if _, err := a.driver.BucketConnection(context.Background(), "forge-other-main-ffff0000"); err == nil {
		t.Fatal("a bucket that is not the deployment's was answered for")
	}
	spaces := NewDriver(Dependencies{Client: a.driver.Client, Slug: "shop", Holdings: a.holdings, Secrets: a.secrets}, Options{})
	if conn, err := spaces.BucketConnection(context.Background(), b.Env["S3_BUCKET"]); err != nil || conn.Host != "syd1.digitaloceanspaces.com" || conn.Port != 443 || conn.SSLMode != "require" {
		t.Fatalf("on Spaces: %+v %v", conn, err)
	}
	delete(a.secrets, SpacesSecretName)
	if _, err := a.driver.BucketConnection(context.Background(), b.Env["S3_BUCKET"]); err == nil {
		t.Fatal("a key whose secret is gone gave a connection")
	}
}

// shopMain is the deployment these tests' bucket and keys are made for:
// they are named by its key, 3b89d1da8075.
var shopMain = deploy.Placement{ID: mustID("01a15e84-c8f1-7000-b757-3b89d1da8075"), Name: "main"}

func mustID(s string) ids.ID {
	id, err := ids.Parse(s)
	if err != nil {
		panic(err)
	}
	return id
}
