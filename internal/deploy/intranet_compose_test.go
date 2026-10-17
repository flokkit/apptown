package deploy

import (
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/appcontract"
	"gopkg.in/yaml.v3"
)

// noSecrets supplies the same empty delivery through the renderer's own port.
type noSecrets struct{}

func (noSecrets) EnvFor(string) map[string]string { return nil }

func TestLanCompose(t *testing.T) {
	t.Parallel()
	one := IntranetCompose(IntranetStack{Secrets: noSecrets{}, Slug: "app", Hosts: []string{"app.apps.example.com"}, Domain: "apps.example.com",
		Image: "reg/app@sha256:x", Port: 8080})
	if !strings.Contains(one, "Host(`app.apps.example.com`)") ||
		strings.Contains(one, "||") || strings.Contains(one, "FORGE_RELEASE") || strings.Contains(one, "volumes:") || strings.Contains(one, "depends_on") {
		t.Fatalf("single-host compose wrong:\n%s", one)
	}
	// Vanity domain: both hosts on one router, extra cert domain, and the
	// dataDir volume + DATA_DIR env (the first server app on the intranet).
	two := IntranetCompose(IntranetStack{Secrets: noSecrets{}, Slug: "app", Hosts: []string{"app.apps.example.com", "feedback.example.com"},
		Domain: "apps.example.com", Image: "reg/app@sha256:x", Port: 8080,
		Env: map[string]string{"LOG": "info"}, DataDir: "/data", Origins: "https://*.apps.example.com", Release: "web@abc"})
	for _, want := range []string{
		"Host(`app.apps.example.com`) || Host(`feedback.example.com`)",
		"tls.domains[0].main: \"*.apps.example.com\"",
		"tls.domains[1].main: \"feedback.example.com\"",
		"forge-data-app:/data",
		`DATA_DIR: "/data"`,
		`FORGE_ORIGINS: "https://*.apps.example.com"`,
		`LOG: "info"`,
		`FORGE_RELEASE: "web@abc"`,
		"loadbalancer.server.port: \"8080\"",
	} {
		if !strings.Contains(two, want) {
			t.Fatalf("compose missing %q:\n%s", want, two)
		}
	}
	// A need-backed app with an init step (Letterbox): the need runs with a
	// persistent volume and a healthcheck, the init step waits for it, the
	// app waits for both — the same order main uses, enforced by compose.
	three := IntranetCompose(IntranetStack{Secrets: noSecrets{}, Slug: "letterbox", Hosts: []string{"letterbox.apps.example.com"}, Domain: "apps.example.com",
		Image: "reg/letterbox:r4", Port: 8080,
		Env: map[string]string{"DATABASE_URL": "postgres://app:app@postgres:5432/app"},
		Needs: []IntranetNeed{{Name: "postgres", Image: "postgres:16-alpine",
			Env: map[string]string{"POSTGRES_DB": "app"}, Health: "pg_isready -U app -d app", Data: "/var/lib/postgresql/data"}},
		Init:  []appcontract.InitContainer{{Name: "migrate", Image: "reg/letterbox:r4", Command: []string{"/app/init.sh"}, Env: map[string]string{"DATABASE_URL": "postgres://app:app@postgres:5432/app"}}},
		Parts: []appcontract.Part{{Name: "worker", Image: "reg/letterbox:r4", Command: []string{"celery"}, Env: map[string]string{"ROLE": "worker", "DATABASE_URL": "postgres://app:app@postgres:5432/app"}}},
	})
	for _, want := range []string{
		"  postgres:\n    image: postgres:16-alpine\n    restart: unless-stopped\n",
		`POSTGRES_DB: "app"`,
		`test: ["CMD-SHELL", "pg_isready -U app -d app"]`,
		"- forge-need-letterbox-postgres:/var/lib/postgresql/data",
		"  init-migrate:\n    image: reg/letterbox:r4\n    restart: \"no\"\n    command: [\"/app/init.sh\"]\n",
		"      postgres:\n        condition: service_healthy\n",
		"      init-migrate:\n        condition: service_completed_successfully\n",
		"      worker:\n        condition: service_started\n",
		"  worker:\n    image: reg/letterbox:r4\n    restart: unless-stopped\n    command: [\"celery\"]\n",
		`ROLE: "worker"`,
		"volumes:\n  forge-need-letterbox-postgres: {}\n",
	} {
		if !strings.Contains(three, want) {
			t.Fatalf("need-backed compose missing %q:\n%s", want, three)
		}
	}
	// The connection env reaches the init step too (migrations need the database).
	if strings.Count(three, `DATABASE_URL: "postgres://app:app@postgres:5432/app"`) != 3 {
		t.Fatalf("DATABASE_URL should reach app, init and worker:\n%s", three)
	}
}

// staticSecrets delivers one secret value to every consumer.
type staticSecrets map[string]string

func (s staticSecrets) EnvFor(string) map[string]string { return s }

// The forge's own values in a stack (the release identity, DATA_DIR,
// FORGE_ORIGINS) win over a contract's or a secret's of the same name, and
// each key appears once: Compose refuses a mapping that repeats one.
func TestAnIntranetStackSetsTheForgesValuesOnceAndLast(t *testing.T) {
	t.Parallel()
	declared := map[string]string{"FORGE_RELEASE": "web@declared", "DATA_DIR": "/elsewhere", "FORGE_ORIGINS": "https://declared"}
	for name, st := range map[string]IntranetStack{
		"contract": {Secrets: noSecrets{}, Env: declared},
		"secret":   {Secrets: staticSecrets(declared)},
	} {
		t.Run(name, func(t *testing.T) {
			st.Slug, st.Hosts, st.Domain, st.Image, st.Port = "app", []string{"app.apps.example.com"}, "apps.example.com", "reg/app@sha256:x", 8080
			st.PublicPart, st.Release, st.DataDir, st.Origins = "web", "web@selected", "/data", "https://*.apps.example.com"
			var stack struct {
				Services map[string]struct {
					Environment map[string]string
				}
			}
			out := IntranetCompose(st)
			if err := yaml.Unmarshal([]byte(out), &stack); err != nil {
				t.Fatalf("the stack is not one Compose accepts: %v\n%s", err, out)
			}
			env := stack.Services["app"].Environment
			if env["FORGE_RELEASE"] != "web@selected" || env["DATA_DIR"] != "/data" || env["FORGE_ORIGINS"] != "https://*.apps.example.com" {
				t.Fatalf("the forge's values did not win: %v", env)
			}
		})
	}
}

// An intranet stack holds each app service to its part's declared
// resources and each need's service to its need's, as Compose reads them,
// and leaves an undeclared part or need without a limit.
func TestAnIntranetStackHoldsEachPartToItsDeclaredResources(t *testing.T) {
	t.Parallel()
	st := IntranetStack{Secrets: noSecrets{}, Slug: "app", Hosts: []string{"app.apps.example.com"}, Domain: "apps.example.com",
		Image: "reg/app@sha256:x", Port: 8080, PublicPart: "web",
		Resources: appcontract.Resources{MemoryBytes: 512 << 20, CPUs: 0.5},
		Needs: []IntranetNeed{{Name: "postgres", Image: "postgres:16", Resources: appcontract.Resources{MemoryBytes: 256 << 20, CPUs: 1}},
			{Name: "redis", Image: "redis:7"}},
		Parts: []appcontract.Part{
			{Name: "worker", Image: "reg/app@sha256:x", Resources: appcontract.Resources{MemoryBytes: 1 << 30}},
			{Name: "cron", Image: "reg/app@sha256:x"},
		}}
	var stack struct {
		Services map[string]struct {
			MemLimit     int64   `yaml:"mem_limit"`
			MemswapLimit int64   `yaml:"memswap_limit"`
			CPUs         float64 `yaml:"cpus"`
		}
	}
	out := IntranetCompose(st)
	if err := yaml.Unmarshal([]byte(out), &stack); err != nil {
		t.Fatalf("the stack is not one Compose accepts: %v\n%s", err, out)
	}
	for name, want := range map[string]struct {
		mem  int64
		cpus float64
	}{"app": {512 << 20, 0.5}, "worker": {1 << 30, 0}, "cron": {0, 0}, "postgres": {256 << 20, 1}, "redis": {0, 0}} {
		got := stack.Services[name]
		if got.MemLimit != want.mem || got.MemswapLimit != want.mem || got.CPUs != want.cpus {
			t.Errorf("%s: mem_limit %d memswap_limit %d cpus %v; want %d and %v\n%s", name, got.MemLimit, got.MemswapLimit, got.CPUs, want.mem, want.cpus, out)
		}
	}
}

// A need started by its own script runs it on Portainer as on a Docker
// host: the entrypoint is replaced, the script's shell variables reach the
// container literally (Compose interpolates $, so they are written $$),
// and the app waits on the need's health, which the script marks.
func TestAnIntranetStackStartsANeedByItsScript(t *testing.T) {
	t.Parallel()
	script := "set -eu\ngateway=$!\nwait \"$gateway\"\n"
	st := IntranetStack{Secrets: noSecrets{}, Slug: "app", Hosts: []string{"app.apps.example.com"}, Domain: "apps.example.com",
		Image: "reg/app@sha256:x", Port: 8080, PublicPart: "web",
		Needs: []IntranetNeed{{Name: "s3", Image: "versity/versitygw:v1.8.0", Entrypoint: []string{"/bin/sh", "-c"}, Command: []string{script},
			Health: "test -f /tmp/forge-ready", Data: "/data"}}}
	var stack struct {
		Services map[string]struct {
			Entrypoint []string `yaml:"entrypoint"`
			Command    []string `yaml:"command"`
			DependsOn  map[string]struct {
				Condition string `yaml:"condition"`
			} `yaml:"depends_on"`
		}
	}
	out := IntranetCompose(st)
	if err := yaml.Unmarshal([]byte(out), &stack); err != nil {
		t.Fatalf("the stack is not one Compose accepts: %v\n%s", err, out)
	}
	s3 := stack.Services["s3"]
	interpolated := strings.ReplaceAll(strings.Join(s3.Command, ""), "$$", "$")
	if !slices.Equal(s3.Entrypoint, []string{"/bin/sh", "-c"}) || len(s3.Command) != 1 || interpolated != script || strings.Contains(strings.ReplaceAll(s3.Command[0], "$$", ""), "$") {
		t.Fatalf("the need's start: entrypoint %q command %q\n%s", s3.Entrypoint, s3.Command, out)
	}
	if got := stack.Services["app"].DependsOn["s3"].Condition; got != "service_healthy" {
		t.Fatalf("the app waits on the store with %q\n%s", got, out)
	}
}
