package deployment

import (
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
)

type consumerEnv map[string]map[string]string

func (c consumerEnv) EnvFor(consumer string) map[string]string { return c[consumer] }

// A need-backed app with an init step: the need runs with a persistent
// volume and a healthcheck, the init step waits for it, the app waits for
// both, expressed as compose conditions so the platform enforces the
// order; secrets reach exactly the consumer they were delivered to.
func TestIntranetComposeOrdersTheStack(t *testing.T) {
	out := deploy.IntranetCompose(deploy.IntranetStack{Slug: "letterbox", Hosts: []string{"letterbox.apps.example.com"}, Domain: "apps.example.com",
		Image: "reg/letterbox:r4", Port: 8080, PublicPart: "web",
		Env:     map[string]string{"DATABASE_URL": "postgres://app:app@postgres:5432/app"},
		Secrets: consumerEnv{"web": {"SECRET_KEY_BASE": "s3"}, "init-migrate": {"MIGRATE_TOKEN": "m1"}},
		Needs: []deploy.IntranetNeed{{Name: "postgres", Image: "postgres:16-alpine", Env: map[string]string{"POSTGRES_DB": "app"},
			Health: "pg_isready -U app -d app", Data: "/var/lib/postgresql/data"}},
		Init:  []appcontract.InitContainer{{Name: "migrate", Image: "reg/letterbox:r4", Command: []string{"/app/init.sh"}}},
		Parts: []appcontract.Part{{Name: "worker", Image: "reg/letterbox:r4", Command: []string{"celery"}, Env: map[string]string{"ROLE": "worker"}}},
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
		`SECRET_KEY_BASE: "s3"`,
		`MIGRATE_TOKEN: "m1"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("compose lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "SECRET_KEY_BASE") != 1 {
		t.Fatalf("a secret reaches only its consumer:\n%s", out)
	}
}

// A release runs from the recorded image: the public part and every part
// or init step built from the same source point at it; a separately built
// one has no recorded image and is refused.
func TestPreparedReleaseApp(t *testing.T) {
	plan := &appcontract.Plan{
		Parts: []appcontract.Part{{Name: "web", Public: true, Build: "."}, {Name: "worker", Build: "."}, {Name: "cache", Image: "redis:7"}},
		Init:  []appcontract.InitContainer{{Name: "migrate", Build: "."}},
	}
	got, err := deploy.PreparedReleaseApp(plan, "reg/app:r9")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got.Parts {
		if (p.Name != "cache" && p.Image != "reg/app:r9") || p.Build != "" {
			t.Fatalf("part %s: %+v", p.Name, p)
		}
	}
	if got.Init[0].Image != "reg/app:r9" || got.Init[0].Build != "" {
		t.Fatalf("init: %+v", got.Init[0])
	}
	if plan.Parts[0].Image != "" {
		t.Fatal("the input plan is not mutated")
	}
	plan.Parts[1].Build = "./worker"
	if _, err := deploy.PreparedReleaseApp(plan, "reg/app:r9"); err == nil || !strings.Contains(err.Error(), `"worker"`) {
		t.Fatalf("a separately built part is refused: %v", err)
	}
}
