package deployment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/oci"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// A release is each distinct build, static site or image reference once,
// under the name of the part (part-<name>) or init step (init-<name>) that
// first declares it; every
// part and init step declaring the same runs that one image. A static
// site's image carries the command serving it on its port, so the same
// folder served on another port is another image.
func TestAReleasePlanBuildsEachDistinctImageOnce(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte(`app:
  web:
    public: true
    static: site
    ports: ["8080"]
  api:
    build: server
  jobs:
    build: server
  cache:
    image: example/cache:7
  mirror:
    static: site
    ports: ["9090"]
init:
  - name: migrate
    build: server
  - name: seed
    image: example/cache:7
  - name: assets
    build: site
`))
	if err != nil {
		t.Fatal(err)
	}
	plan := PlanRelease(c)
	want := ReleasePlan{
		Images: []releaseImage{{Name: "part-api", Build: "server"}, {Name: "part-cache", Source: "example/cache:7"}, {Name: "part-mirror", Static: "site", Port: 9090}, {Name: "part-web", Static: "site", Port: 8080}, {Name: "init-assets", Build: "site"}},
		Parts:  map[string]string{"web": "part-web", "mirror": "part-mirror", "api": "part-api", "jobs": "part-api", "cache": "part-cache"},
		Init:   map[string]string{"migrate": "part-api", "seed": "part-cache", "assets": "init-assets"},
	}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("the plan:\n got %+v\nwant %+v", plan, want)
	}
}

// layoutFiles is an OCI image layout in memory (oci.Layout).
type layoutFiles map[string][]byte

func (f layoutFiles) Open(_ context.Context, name string) (io.ReadCloser, error) {
	b, ok := f[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

// ociImage is one linux/amd64 image whose one layer is content.
func ociImage(t *testing.T, content string) layoutFiles {
	t.Helper()
	files := layoutFiles{}
	put := func(mediaType string, b []byte) map[string]any {
		sum := sha256.Sum256(b)
		files["blobs/sha256/"+hex.EncodeToString(sum[:])] = b
		return map[string]any{"mediaType": mediaType, "digest": "sha256:" + hex.EncodeToString(sum[:]), "size": len(b)}
	}
	encode := func(v any) []byte {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	layer := put("application/vnd.oci.image.layer.v1.tar", []byte(content))
	config := put("application/vnd.oci.image.config.v1+json", encode(map[string]any{"architecture": "amd64", "os": "linux", "rootfs": map[string]any{"type": "layers", "diff_ids": []any{layer["digest"]}}}))
	manifest := put("application/vnd.oci.image.manifest.v1+json", encode(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "config": config, "layers": []any{layer}}))
	files["index.json"] = encode(map[string]any{"schemaVersion": 2, "manifests": []any{manifest}})
	return files
}

// A release is published image by image, each only while the deploy step
// attempt still owns its step: once a later attempt has superseded it, no
// further image is written and no bundle is committed. Published by the
// attempt that owns its step, the bundle names every image by the digest
// the registry reads back, in the project's registry, tagged by change and
// image (a later attempt moves the tag to its own build).
func TestAPublicationStopsWhenItsAttemptIsSuperseded(t *testing.T) {
	t.Parallel()
	w := noLocalDockerWorld(t, multipartApp)
	s := w.service(false)
	reg := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(reg.Close)
	host := strings.TrimPrefix(reg.URL, "http://")
	if err := s.env.integrations.Attachments.Attach(t.Context(), w.project, "container-registry", "missing", `{"repo":"`+host+`/team/game"}`, "test", nil); err != nil {
		t.Fatal(err)
	}
	c, err := appcontract.Parse([]byte("app:\n  web:\n    public: true\n    static: site\n    ports: [\"8080\"]\n  worker:\n    build: worker\ninit:\n  - name: migrate\n    build: migrations\n"))
	if err != nil {
		t.Fatal(err)
	}
	plan := PlanRelease(c)
	built := map[string]oci.Layout{}
	for _, img := range plan.Images {
		built[img.Name] = ociImage(t, img.Name)
	}
	publish := func(claim *claimedAttempt) (*ReleaseBundle, error) {
		return s.PublishRelease(context.Background(), ReleasePublication{By: claim, ChangeID: idstest.N(41), Tree: w.revision, Contract: []byte("app: {}"), Plan: plan, Built: built,
			Platform: "linux/amd64", Build: BundleBuild{Worker: "https://worker:7070", Role: "agent"}})
	}
	tagged := func(tag string) string {
		ref, err := name.NewTag(host + "/team/game:" + tag)
		if err != nil {
			t.Fatal(err)
		}
		d, err := remote.Head(ref)
		if err != nil {
			return ""
		}
		return d.Digest.String()
	}

	// Plan order: part-web, part-worker, then init-migrate (parts sorted by
	// name).
	superseded := &claimedAttempt{db: w.db, project: w.project, run: idstest.N(7), step: idstest.N(70), at: 1, lostAfter: 2}
	if b, err := publish(superseded); !errors.Is(err, errAttemptLost) || b != nil {
		t.Fatalf("a superseded attempt published %+v: %v", b, err)
	}
	if tagged("change-"+idstest.N(41).String()+"-"+plan.Images[0].Name) == "" || tagged("change-"+idstest.N(41).String()+"-"+plan.Images[1].Name) == "" {
		t.Fatal("the images before the attempt was superseded were not written")
	}
	repo, err := name.NewRepository(host + "/team/game")
	if err != nil {
		t.Fatal(err)
	}
	tags, err := remote.List(repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range tags {
		if strings.HasSuffix(tag, "-"+plan.Images[2].Name) {
			t.Errorf("an image was written after the attempt was superseded: %s", tag)
		}
	}
	if b, err := s.Bundles.Latest(context.Background(), w.project); err != nil || b != nil {
		t.Fatalf("a superseded attempt committed %+v %v", b, err)
	}

	b, err := publish(&claimedAttempt{db: w.db, project: w.project, run: idstest.N(7), step: idstest.N(70), at: 2})
	if err != nil {
		t.Fatal(err)
	}
	if b.BuiltFrom.Registry != host+"/team/game" || b.BuiltFrom.Attempt != 2 || b.ContractRevision != w.revision {
		t.Errorf("the bundle: %+v", b)
	}
	for part, img := range plan.Parts {
		if want := host + "/team/game@" + tagged("change-"+idstest.N(41).String()+"-"+img); b.Parts[part] != want {
			t.Errorf("the bundle's %s is %s; the registry answers %s", part, b.Parts[part], want)
		}
	}
	if want := host + "/team/game@" + tagged("change-"+idstest.N(41).String()+"-init-migrate"); b.Init["migrate"] != want {
		t.Errorf("the bundle's migrate step is %s; the registry answers %s", b.Init["migrate"], want)
	}
}

// A part and an init step are different images, one built and one
// image-only; each keeps its own published digest through publication, and
// neither is committed as the other. (Names that would meet, a part named
// init-migrate beside an init step migrate, are refused by the contract.)
//
// Kills: one namespace for parts and init steps (the init image's copy
// overwriting the part's publication).
func TestAPartAndAnInitStepKeepTheirOwnImages(t *testing.T) {
	w := noLocalDockerWorld(t, multipartApp)
	s := w.service(false)
	reg := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(reg.Close)
	repo := strings.TrimPrefix(reg.URL, "http://") + "/team/game"
	if err := s.env.integrations.Attachments.Attach(t.Context(), w.project, "container-registry", "missing", `{"repo":"`+repo+`"}`, "test", nil); err != nil {
		t.Fatal(err)
	}
	initImage, err := oci.Publish(context.Background(), ociImage(t, "migration image"), oci.Repository{Name: repo}, "migration", "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("app:\n  web:\n    public: true\n    build: app\ninit:\n  - name: migrate\n    image: " + initImage.Reference() + "\n")
	c, err := appcontract.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	plan := PlanRelease(c)
	appLayout := ociImage(t, "application image")
	appImage, err := oci.Publish(context.Background(), appLayout, oci.Repository{Name: repo}, "expected-app", "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	built := map[string]oci.Layout{}
	for _, img := range plan.Images {
		if img.Build != "" {
			built[img.Name] = appLayout
		}
	}
	b, err := s.PublishRelease(context.Background(), ReleasePublication{
		By:   &claimedAttempt{db: w.db, project: w.project, run: idstest.N(7), step: idstest.N(70), at: 1},
		Tree: w.revision, Contract: raw, Plan: plan,
		Built:    built,
		Platform: "linux/amd64", Build: BundleBuild{Worker: "https://worker:7070", Role: "agent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.Parts["web"] != appImage.Reference() || b.Init["migrate"] != initImage.Reference() {
		t.Fatalf("distinct images collapsed: app=%s init=%s; expected app=%s init=%s", b.Parts["web"], b.Init["migrate"], appImage.Reference(), initImage.Reference())
	}
}

// Retagging an old bundle must not retarget a newly attached registry's
// credential. The fake old registry asks for Basic authentication; no real
// registry, image or credential is involved.
func TestHistoricalReleaseTagDoesNotRetargetTheCurrentRegistryCredential(t *testing.T) {
	t.Parallel()
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprint(same), func(t *testing.T) {
			var offered, requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, password, _ := r.BasicAuth()
				if password == "new-registry-canary" {
					offered.Add(1)
				}
				w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
				w.WriteHeader(http.StatusUnauthorized)
			}))
			t.Cleanup(server.Close)
			oldHost := strings.TrimPrefix(server.URL, "http://")
			currentHost := oldHost
			if !same {
				currentHost = "new.registry.example"
			}
			db := testDB(t)
			id := createTestProject(t, db, "shop", "Shop")
			projects := project.New(db, nil)
			integrations := integration.NewService(db, integration.Dependencies{Projects: projects})
			if _, err := integrations.Bindings.Save(t.Context(), "registry", integration.CapContainerRegistry, "service", fmt.Sprintf(`{"url":%q}`, currentHost), "fixture"); err != nil {
				t.Fatal(err)
			}
			resource := fmt.Sprintf(`{"repo":%q,"username":"robot","token":"new-registry-canary"}`, currentHost+"/team/shop")
			if err := integrations.Attachments.Attach(t.Context(), id, integration.CapContainerRegistry, "registry", resource, "fixture", nil); err != nil {
				t.Fatal(err)
			}
			s := &Service{env: NewEnvironment(EnvironmentDependencies{Projects: projects, Integrations: integrations})}
			digest := "sha256:" + strings.Repeat("a", 64)
			bundle := &ReleaseBundle{ProjectID: id, BuiltFrom: BundleOrigin{Registry: oldHost + "/team/shop"}, Parts: map[string]string{"web": oldHost + "/team/shop@" + digest}}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			p := &Providers{env: s.env}
			selected, err := p.releaseCredential(ctx, id, bundle.Parts["web"])
			if err != nil || (selected.Password == "new-registry-canary") != same {
				t.Fatalf("historical provider credential selected=%v want=%v err=%v", selected.Password != "", same, err)
			}
			s.tagRelease(ctx, 1, bundle, func(string, ...any) {})
			if same && offered.Load() == 0 {
				t.Fatal("same-registry control never offered its credential")
			}
			if !same && requests.Load() != 0 {
				t.Fatalf("out-of-scope historical registry received %d requests", requests.Load())
			}
		})
	}
}
