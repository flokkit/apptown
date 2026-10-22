package deployment

import (
	"context"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/stock"
)

const resolvedStackFixture = `app:
  web:
    image: reg.example/app:1
    ports: ["8080"]
    public: true
    dataDir: /data
  worker:
    image: reg.example/worker:1
    command: ["node", "worker.js"]
init:
  - name: migrate
    image: reg.example/app:1
    command: ["./migrate.sh"]
needs:
  postgres: {}
`

// The intranet release renders the stack bootStack resolves — the same
// needs, the same merged env — rather than resolving on its own.
func TestResolveStackIsWhatTheReleaseRenders(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	catalog := dependency.NewCatalog(db, nil)
	if _, _, err := catalog.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	owner := New(context.Background(), db, assembled(db, testRuntime{Needs: catalog}, Dependencies{}))
	c, err := appcontract.Parse([]byte(resolvedStackFixture))
	if err != nil {
		t.Fatal(err)
	}
	rs, err := owner.env.ResolveDockerStack(t.Context(), c, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Services) != 1 || len(rs.Needs) != 1 || rs.Needs[0].Name != "postgres" ||
		rs.Needs[0].Data != "/var/lib/postgresql/data" || rs.Needs[0].Health == "" {
		t.Fatalf("resolved: services %+v needs %+v", rs.Services, rs.Needs)
	}
	if rs.Plan.PublicPart().Env["DATABASE_URL"] != "postgres://app:app@postgres:5432/app?sslmode=disable" {
		t.Fatalf("the plan must carry the need's connection env: %v", rs.Plan.PublicPart().Env)
	}
	if c.PublicPart().Env["DATABASE_URL"] != "" {
		t.Fatal("resolving must not mutate the input contract")
	}
	compose := deploy.IntranetCompose(deploy.IntranetStack{Prefix: "apptown-", Secrets: secret.Delivery{}, Base: "letterbox", Hosts: []string{"letterbox.apps.example.com"}, Domain: "apps.example.com",
		Image: "reg/letterbox:r5", Port: c.PublicPart().Port, Env: rs.Plan.PublicPart().Env, Needs: docker.IntranetNeeds(rs.Needs), Init: rs.Plan.Init})
	for _, want := range []string{
		"  postgres:\n    image: postgres:16-alpine\n",
		"- apptown-need-letterbox-postgres:/var/lib/postgresql/data",
		`DATABASE_URL: "postgres://app:app@postgres:5432/app?sslmode=disable"`,
		"  init-migrate:\n",
	} {
		if !strings.Contains(compose, want) {
			t.Fatalf("compose missing %q:\n%s", want, compose)
		}
	}
	// Workflow services accompany the native contract's declared needs;
	// the needs still ride behind it.
	rs, _ = owner.env.ResolveDockerStack(t.Context(), c, []deploy.ServiceSpec{{Name: "cache", Image: "redis:7"}}, nil)
	if len(rs.Services) != 2 || rs.Services[0].Name != "cache" || rs.Services[1].Name != "postgres" {
		t.Fatalf("override then needs: %+v", rs.Services)
	}
	if _, err := owner.env.ResolveDockerStack(t.Context(), &appcontract.Contract{Needs: []string{"quantumdb"}}, nil, nil); err == nil ||
		err.Error() != "this app needs quantumdb, which is not in the dependency catalog" {
		t.Fatalf("unknown need: %v", err)
	}
}
