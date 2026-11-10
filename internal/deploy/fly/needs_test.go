package fly

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/fly/flyfake"
	"github.com/flokkit/apptown/internal/oci"
	"gopkg.in/yaml.v3"
)

// catalogueNeeds serves the stock Postgres kind's local container as the
// catalogue runtime would: its image, start script and whole readiness
// command come from the shipped file, so the tests run what Fly would. The
// catalogue's own resolution is a library above this one (layering); the
// assembled release test in deployment exercises it end to end.
func catalogueNeeds(t *testing.T, w *world) {
	t.Helper()
	b, err := os.ReadFile("../../../stock/dependencies/postgres.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var kind struct {
		Local struct {
			Image         string   `yaml:"image"`
			Entrypoint    []string `yaml:"entrypoint"`
			Command       []string `yaml:"command"`
			Health        string   `yaml:"health"`
			Data          string   `yaml:"data"`
			ConfigAtStart bool     `yaml:"configAtStart"`
			KeepsImage    bool     `yaml:"keepsImage"`
		} `yaml:"local"`
	}
	if err := yaml.Unmarshal(b, &kind); err != nil {
		t.Fatal(err)
	}
	l := kind.Local
	if l.Health == "" || len(l.Command) == 0 || l.Data == "" {
		t.Fatal("the stock Postgres kind lost its local container")
	}
	w.driver.Needs = func(ctx context.Context, c *appcontract.Contract, hosts map[string]string) ([]deploy.ServiceSpec, map[string]string, error) {
		version := c.NeedParams["postgres"]["version"]
		if version == "" {
			version = "16"
		}
		s := deploy.ServiceSpec{Name: "postgres", Image: strings.ReplaceAll(l.Image, "{version}", version), Entrypoint: l.Entrypoint, Command: l.Command, Health: l.Health, Data: l.Data, ConfigAtStart: l.ConfigAtStart, KeepsImage: l.KeepsImage, Credentials: []string{"generated-secret"},
			Env:       map[string]string{"POSTGRES_DB": "app", "POSTGRES_USER": "app", "POSTGRES_PASSWORD": "generated-secret", "APP_SUPERUSER": "false"},
			Resources: appcontract.Resources{MemoryBytes: 256 << 20}}
		if r, ok := c.NeedResources["postgres"]; ok {
			s.Resources = r
		}
		h := hosts["postgres"]
		inject := map[string]string{"PGHOST": h, "PGPORT": "5432", "PGUSER": "app", "PGPASSWORD": "generated-secret", "PGDATABASE": "app", "PGSSLMODE": "disable",
			"DATABASE_URL": "postgres://app:generated-secret@" + h + ":5432/app?sslmode=disable"}
		return []deploy.ServiceSpec{s}, inject, nil
	}
	w.driver.options.CopyImage = func(_ context.Context, from oci.Source, _ oci.Repository, _ string) (string, error) {
		if strings.Contains(from.Reference, "@") {
			_, d, _ := strings.Cut(from.Reference, "@")
			return d, nil
		}
		return digestOf("p"), nil
	}
}

func needMachine(t *testing.T, a *flyfake.App) *flyfake.Machine {
	t.Helper()
	for _, m := range a.Machines {
		if m.Metadata()[metaRole] == "need" {
			return m
		}
	}
	t.Fatal("no need Machine")
	return nil
}

func TestStandingNeedKeepsItsAddressAndMountAfterSizeChangeAndFailedInit(t *testing.T) {
	w := newWorld(t)
	catalogueNeeds(t, w)
	src := source(t, webApp+"needs: {postgres: {}}\n", "a")
	if _, err := w.release(src, 1, nil); err != nil {
		t.Fatal(err)
	}
	app := w.api.App(appName(installation, deploy.HostKey(w.p.ID)))
	old := needMachine(t, app)
	host := old.ID + ".vm." + app.Name + ".internal"
	if app.Secrets["PGHOST"] != host || !strings.Contains(app.Secrets["DATABASE_URL"], "@"+host+":") {
		t.Fatalf("bad connection: %v", app.Secrets)
	}
	if old.Metadata()[metaSourceImage] != "postgres:16-alpine" {
		t.Fatal("catalogue image missing")
	}
	// initdb refuses a non-empty directory: the cluster's folder may not be
	// the volume's root, which holds lost+found on Fly.
	mt := old.Config["mounts"].([]any)[0].(map[string]any)
	pgdata, _ := old.Config["env"].(map[string]any)["PGDATA"].(string)
	if pgdata == "" {
		pgdata = mt["path"].(string) // the image's default is the declared data folder
	}
	if root := app.Volumes[mt["volume"].(string)].Root; len(root) == 0 || !strings.HasPrefix(pgdata, mt["path"].(string)+"/") {
		t.Fatalf("Postgres's data folder %s is the volume root, which holds %v", pgdata, root)
	}
	init := old.Config["init"].(map[string]any)
	if !strings.Contains(init["cmd"].([]any)[0].(string), "touch /tmp/forge-ready") {
		t.Fatal("catalogue start script missing")
	}
	w.api.OnCreate = func(_ string, m *flyfake.Machine) {
		if m.Metadata()[metaRole] == "init" {
			flyfake.Exit(m, 1, false)
		}
	}
	src = source(t, webApp+"needs: {postgres: {resources: {memory: 1G}}}\ninit:\n  - {name: migrate, image: reg/web:1}\n", "b")
	if _, err := w.release(src, 2, nil); err == nil {
		t.Fatal("init failure passed")
	}
	app = w.api.App(app.Name)
	now := needMachine(t, app)
	if now.ID != old.ID || now.Config["guest"].(map[string]any)["memory_mb"] != float64(1024) {
		t.Fatalf("need was replaced or not resized: %v", now)
	}
	if now.Config["mounts"].([]any)[0].(map[string]any)["volume"] != old.Config["mounts"].([]any)[0].(map[string]any)["volume"] {
		t.Fatal("need lost mount")
	}
	if app.Secrets["PGHOST"] != host || !strings.Contains(app.Secrets["DATABASE_URL"], "@"+host+":") {
		t.Fatal("retained release lost connection")
	}
	for _, m := range app.Machines {
		if m.Metadata()[metaRole] == "part" && m.Metadata()[metaGeneration] != "1" {
			t.Fatal("previous part not retained")
		}
	}
	before := len(w.api.Requests())
	src = source(t, webApp+"needs: {postgres: {version: '17'}}\n", "c")
	if _, err := w.release(src, 3, nil); err == nil {
		t.Fatal("major version change passed")
	}
	for _, r := range w.api.Requests()[before:] {
		if r.Method != "GET" {
			t.Fatalf("incompatible version changed provider: %v", r)
		}
	}
}

func TestNeedWaitsForWholeCatalogueReadinessBeforeInitOrParts(t *testing.T) {
	for _, mode := range []string{"held", "error", "absent"} {
		t.Run(mode, func(t *testing.T) {
			w := newWorld(t)
			catalogueNeeds(t, w)
			w.driver.options.machineWait = 80 * time.Millisecond
			var calls atomic.Int32
			w.api.OnExec = func(_ string, m *flyfake.Machine, cmd []string) (map[string]any, int) {
				if len(cmd) != 3 || cmd[0] != "/bin/sh" || cmd[1] != "-c" || !strings.Contains(cmd[2], "test -f /tmp/forge-ready && pg_isready") {
					t.Error("whole catalogue health must run through a shell")
				}
				n := calls.Add(1)
				if mode == "error" {
					return nil, 503
				}
				if mode == "absent" {
					return nil, 200
				}
				if n < 4 {
					return map[string]any{"exit_code": 1}, 200
				}
				return map[string]any{"exit_code": 0}, 200
			}
			w.api.OnCreate = func(_ string, m *flyfake.Machine) {
				if m.Metadata()[metaRole] != "need" && calls.Load() < 4 {
					t.Error("init or part ran before need readiness")
				}
			}
			_, err := w.release(source(t, webApp+"needs: {postgres: {}}\ninit:\n  - {name: migrate, image: reg/web:1}\n", "a"), 1, nil)
			if mode == "held" && err != nil {
				t.Fatal(err)
			}
			if mode != "held" && err == nil {
				t.Fatal("missing readiness evidence passed")
			}
			if calls.Load() == 0 {
				t.Fatal("readiness never executed")
			}
		})
	}
}

func TestNeedSecretsDoNotReplaceOrdinaryPartEnvironmentAndReachInitAsSecrets(t *testing.T) {
	w := newWorld(t)
	catalogueNeeds(t, w)
	yaml := "version: 2\napp:\n  web:\n    image: reg/web:1\n    public: true\n    ports: ['8080']\n    environment: {LABEL: app-web, APP_DSN: '${needs.postgres.DATABASE_URL}'}\n  worker:\n    image: reg/web:1\n    environment: {LABEL: app-worker}\nneeds: {postgres: {}}\ninit:\n  - name: migrate\n    image: reg/web:1\n    environment: {APP_DSN: '${needs.postgres.DATABASE_URL}'}\n"
	initSeen := false
	w.api.OnCreate = func(_ string, m *flyfake.Machine) {
		if m.Metadata()[metaRole] == "init" {
			initSeen = true
			env := m.Config["env"].(map[string]any)
			if env["PGPASSWORD"] != nil || env["APP_DSN"] != nil {
				t.Error("need credentials entered init config instead of Fly secrets")
			}
		}
	}
	if _, err := w.release(source(t, yaml, "a"), 1, nil); err != nil {
		t.Fatal(err)
	}
	if !initSeen {
		t.Fatal("init did not run")
	}
	app := w.api.App(appName(installation, deploy.HostKey(w.p.ID)))
	if app.Secrets["LABEL"] != "" {
		t.Fatal("ordinary per-part value became a global secret")
	}
	if !strings.Contains(app.Secrets["APP_DSN"], "generated-secret") {
		t.Fatal("wired credential was not delivered as a secret")
	}
	for _, m := range app.Machines {
		if m.Metadata()[metaRole] == "part" {
			env := m.Config["env"].(map[string]any)
			if env["LABEL"] != "app-"+m.Metadata()[metaGroup] {
				t.Fatalf("part environment changed: %v", env)
			}
			if env["PGPASSWORD"] != nil || env["APP_DSN"] != nil {
				t.Error("credentials entered part config")
			}
		}
	}
}
