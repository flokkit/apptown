package docker

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// storeNeed is a catalogue with one need, an object store started by its
// own script, which sets its configuration at every start.
type storeNeed struct{}

func (storeNeed) Fulfil(ctx context.Context, needs []string, _ map[string]map[string]string) ([]deploy.ServiceSpec, map[string]string, error) {
	if !slices.Contains(needs, "s3") {
		return nil, nil, nil
	}
	return []deploy.ServiceSpec{{Name: "s3", Image: "gateway:2", Entrypoint: []string{"/bin/sh", "-c"}, Command: []string{"start the store"},
		Env: map[string]string{"ROOT_ACCESS_KEY_ID": "forge-root"}, Health: "test -f /tmp/ready", ConfigAtStart: true}}, map[string]string{"S3_BUCKET": "app"}, nil
}
func (storeNeed) Data(context.Context, string) (string, error) { return "/data", nil }

// storeRelease releases an app needing the store to a deployment whose
// standing store was made from another image, entrypoint and environment
// (an older s3.yaml), with health answering health.
func storeRelease(t *testing.T, class, health string) (*ownedHost, error) {
	t.Helper()
	cname := Current.Service("env-shop-main", "s3")
	vol := Current.NeedVolume("env-shop-main", "s3")
	h := newOwnedHost("container "+cname, "volume "+vol, "network apptown-net-env-shop-main")
	client := &fakeClient{reply: func(req Command) ([]byte, error) {
		line := strings.Join(req.Args, " ")
		switch {
		case strings.Contains(line, "{{.Config.Image}}"):
			return []byte("gateway:1\n"), nil
		case strings.Contains(line, "{{json .Config.Entrypoint}}"):
			return []byte(`["/usr/local/bin/docker-entrypoint.sh"]`), nil
		case strings.Contains(line, "{{json .Config.Cmd}}"):
			return []byte(`["--port",":9000","posix","/data"]`), nil
		case strings.Contains(line, "{{range .Config.Env}}"):
			return []byte("ROOT_ACCESS_KEY_ID=local\n"), nil
		case strings.Contains(line, ".State.Health"):
			return []byte(health + "\n"), nil
		case strings.Contains(line, ".Mounts"):
			// Its objects are on its named volume.
			return []byte("volume\t" + vol + "\t/x\t/data\n"), nil
		case strings.HasPrefix(line, "image inspect --format {{.Id}}"):
			return []byte("sha256:" + strings.Repeat("a", 64) + "\n"), nil
		}
		return h.reply(req)
	}}
	d := &Driver{
		Scope: func(context.Context) (Scope, error) { return Scope{Client: client}, nil },
		Boot:  NewHost(Dependencies{Needs: storeNeed{}, Secrets: reviewNoSecrets{}}),
		URL:   func(_, _ string, port int) string { return fmt.Sprintf("http://127.0.0.1:%d", port) },
		Owner: recordNothing{},
		Owned: func(context.Context) ([]deploy.Owned, error) {
			return []deploy.Owned{{Kind: "volume", ID: vol, Data: true, Need: "s3"}}, nil
		},
		DataClass: class,
	}
	c, err := appcontract.Parse([]byte("app:\n  web:\n    image: app:1\n    ports: [\"8080\"]\n    public: true\ninit:\n  - name: migrate\n    image: app:1\n    command: [\"migrate\"]\nneeds:\n  s3: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"version":1,"revision":"abc"}`)
	art := deploy.Artifact{Kind: "docker-source", Body: body, Hash: deploy.ConfigHash(string(body)),
		Source: &deploy.Source{Workspace: t.TempDir(), Revision: "abc", Slug: "shop", Contract: c}}
	_, err = d.Reconcile(context.Background(), deploy.Placement{HostPrefix: "apptown-", ID: idstest.N(7), Label: "7", Name: "main"}, 2, art, "key", func(string, ...any) {}, func(map[string]any) error { return nil })
	return h, err
}

// A standing store that sets its configuration at every start, made by an
// older catalogue entry, is remade on its own volume whatever the
// deployment's data, started by its script (the entrypoint replaced): the
// objects stay. A database's drift is refused (TestAReleaseThroughTheDriverResetsOnlySyntheticData).
func TestAStoreThatSetsItsConfigurationAtStartIsRemadeOnItsVolume(t *testing.T) {
	for _, class := range []string{"production", "non-production", ""} {
		t.Run(fmt.Sprintf("class %q", class), func(t *testing.T) {
			t.Parallel()
			h, err := storeRelease(t, class, "healthy")
			// The fake host gives the app no port: the release ends there,
			// past its needs.
			if err != nil && AsBootError(err).Phase == PhaseNeeds {
				t.Fatalf("the store was not remade: %v", err)
			}
			cname := Current.Service("env-shop-main", "s3")
			if !slices.Contains(h.removed, "container "+cname) || slices.Contains(h.removed, "volume "+Current.NeedVolume("env-shop-main", "s3")) {
				t.Fatalf("remade %v: the container goes, the volume stays", h.removed)
			}
			var store []string
			for _, run := range h.runs {
				if slices.Contains(run, cname) {
					store = run
				}
			}
			line := strings.Join(store, " ")
			if !strings.Contains(line, "--entrypoint /bin/sh") || !strings.HasSuffix(line, "gateway:2 -c start the store") || !strings.Contains(line, "-v "+Current.NeedVolume("env-shop-main", "s3")+":/data") {
				t.Fatalf("the store's start: %v", store)
			}
		})
	}
}

// A need that is not healthy fails the release, naming it, before any init
// step or part of the app starts: its health check says it is ready for the
// app (a store's bucket and account are made).
func TestANeedThatIsNotHealthyStopsTheReleaseBeforeTheApp(t *testing.T) {
	t.Parallel()
	h, err := storeRelease(t, "production", "unhealthy")
	if err == nil || !strings.Contains(err.Error(), "the s3 service is unhealthy") {
		t.Fatalf("an unhealthy store: %v", err)
	}
	for _, run := range h.runs {
		if slices.Contains(run, "app:1") {
			t.Fatalf("the app started beside an unhealthy store: %v", run)
		}
	}
}

// A preview's throwaway needs are started the same way and fail the same
// way: the start script replaces the entrypoint, and a need that is not
// healthy stops the boot with its stack removed.
func TestAThrowawayNeedStartsByItsScriptAndStopsTheBootWhenNotHealthy(t *testing.T) {
	t.Parallel()
	for _, health := range []string{"healthy", "unhealthy"} {
		t.Run(health, func(t *testing.T) {
			t.Parallel()
			h := newOwnedHost()
			client := &fakeClient{reply: func(req Command) ([]byte, error) {
				if strings.Contains(strings.Join(req.Args, " "), ".State.Health") {
					return []byte(health + "\n"), nil
				}
				return h.reply(req)
			}}
			svcs, _, _ := storeNeed{}.Fulfil(t.Context(), []string{"s3"}, nil)
			_, cleanup, err := StartServices(t.Context(), Scope{Client: client}, "preview-shop-1", svcs)
			cleanup()
			if health == "unhealthy" {
				if err == nil || !strings.Contains(err.Error(), "the s3 service is unhealthy") {
					t.Fatalf("an unhealthy throwaway store: %v", err)
				}
				return
			}
			if err != nil || len(h.runs) != 1 || !strings.HasSuffix(strings.Join(h.runs[0], " "), "--entrypoint /bin/sh gateway:2 -c start the store") {
				t.Fatalf("the throwaway store's start: %v %v", err, h.runs)
			}
		})
	}
}

// An entrypoint the declaration names is compared with the standing
// container's: a changed start script is drift.
func TestAChangedEntrypointIsDrift(t *testing.T) {
	t.Parallel()
	for _, have := range []string{`["/bin/sh"]`, `["/usr/local/bin/docker-entrypoint.sh"]`} {
		client := &fakeClient{reply: func(req Command) ([]byte, error) {
			line := strings.Join(req.Args, " ")
			switch {
			case strings.Contains(line, "Config.Entrypoint"):
				return []byte(have), nil
			case strings.Contains(line, "Config.Cmd"):
				return []byte(`["-c","start"]`), nil
			}
			return nil, nil
		}}
		drift, err := ServiceDrift(t.Context(), Scope{Client: client}, "s3", deploy.ServiceSpec{Name: "s3", Entrypoint: []string{"/bin/sh", "-c"}, Command: []string{"start"}})
		if err != nil || slices.Contains(drift, "entrypoint") != (have != `["/bin/sh"]`) || slices.Contains(drift, "command") {
			t.Fatalf("standing %s: drift %v %v", have, drift, err)
		}
	}
}

// A service that sets its configuration at every start and keeps its data
// in its image's own format (Postgres) is remade on its volume when its
// start script or environment moved, but its image moving (another major
// version, which cannot read the old files) is refused for real data as
// any data service's is. One that keeps nothing of its image in its data
// (the S3 store) is remade whatever moved.
func TestAServiceThatKeepsItsImageIsNotRemadeForANewOne(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name       string
		keepsImage bool
		drift      []string
		remade     bool
	}{
		{"its start script and password moved", true, []string{"entrypoint", "POSTGRES_PASSWORD"}, true},
		{"its image moved", true, []string{"image", "entrypoint"}, false},
		{"a store's image moved", false, []string{"image", "entrypoint"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var removed []string
			client := &fakeClient{reply: func(req Command) ([]byte, error) {
				if len(req.Args) > 0 && req.Args[0] == "rm" {
					removed = append(removed, strings.Join(req.Args, " "))
				}
				return nil, nil
			}}
			svc := deploy.ServiceSpec{Name: "postgres", Image: "postgres:17-alpine", ConfigAtStart: true, KeepsImage: c.keepsImage}
			_, err := replaceDrifted(t.Context(), Scope{Client: client}, "env-shop-main", Current.Service("env-shop-main", "postgres"), svc, c.drift, "production", Ownership{}, nil)
			if c.remade {
				if err != nil || len(removed) != 1 {
					t.Fatalf("not remade on its volume: %v %v", err, removed)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "created with other parameters") || len(removed) != 0 {
				t.Fatalf("a new image on a database's files was not refused: %v %v", err, removed)
			}
		})
	}
}
