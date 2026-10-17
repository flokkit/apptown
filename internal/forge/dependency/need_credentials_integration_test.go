//go:build integration

package dependency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockertest"
	"flokkit.io/forge/internal/poll"
	"flokkit.io/forge/stock"
)

// A standing need made by rc.5's catalogue entry (testdata/rc5: the
// catalogue's constant credentials, or none) holding data takes the
// deployment's own generated credentials at its next deploy: it is remade
// on its own volume rather than refused, its data is there, the old
// credential is refused and the new one works.

// upgradeKinds are a kind as rc.5 shipped it and as the stock catalogue
// has it now.
func upgradeKinds(t *testing.T, capability string) (old, current *Kind) {
	t.Helper()
	parse := func(raw []byte, err error) *Kind {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		k, err := Parse(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	return parse(os.ReadFile("testdata/rc5/" + capability + ".yaml")), parse(stock.FS.ReadFile("dependencies/" + capability + ".yaml"))
}

// testCredentials stand in for a deployment's derived credentials: 64 hex
// characters, distinct for each credential and each test.
func testCredentials(t *testing.T) Credentials {
	return func(_ context.Context, capability, name string) (string, error) {
		sum := sha256.Sum256([]byte(t.Name() + "/" + capability + "." + name))
		return hex.EncodeToString(sum[:]), nil
	}
}

// upgradeStack is a deployment's standing need on a stack of its own,
// removed with its volume and network when the test ends.
type upgradeStack struct {
	t     *testing.T
	stack string
	name  string
	dk    docker.Scope
	own   docker.Ownership
}

func newUpgradeStack(t *testing.T, need, data string) *upgradeStack {
	t.Helper()
	u := &upgradeStack{t: t, stack: fmt.Sprintf("env-creds%s%d-main", need, os.Getpid()), name: need, dk: docker.Scope{Client: docker.CLI{Binary: "docker"}},
		own: docker.Ownership{Record: func(deploy.Owned) error { return nil }, Needs: map[string]string{need: data}}}
	t.Cleanup(func() {
		_ = dockertest.Run("rm", "-f", docker.ServiceName(u.stack, need))
		_ = dockertest.Run("volume", "rm", docker.NeedVolumeName(u.stack, need))
		_ = dockertest.Run("network", "rm", docker.NetworkName(u.stack))
	})
	return u
}

// ensure starts (or remakes) the standing service as a deploy of real data does.
func (u *upgradeStack) ensure(spec deploy.ServiceSpec) {
	u.t.Helper()
	if _, err := docker.EnsureStandingServices(u.t.Context(), u.dk, u.stack, []deploy.ServiceSpec{spec}, "production", u.own); err != nil {
		u.t.Fatalf("the standing %s: %v", u.name, err)
	}
}

// healthy waits for the service's own health check.
func (u *upgradeStack) healthy() {
	u.t.Helper()
	health := ""
	if !poll.Until(u.t.Context(), 120*time.Second, 250*time.Millisecond, func() bool {
		out, _ := dockertest.Output("inspect", "-f", "{{.State.Health.Status}}", docker.ServiceName(u.stack, u.name))
		health = strings.TrimSpace(string(out))
		return health == "healthy"
	}) {
		logs, _ := dockertest.CombinedOutput("logs", "--tail", "30", docker.ServiceName(u.stack, u.name))
		u.t.Fatalf("the %s did not become healthy: %s\n%s", u.name, health, logs)
	}
}

// client runs a command in image on the stack's network: never inside the
// service, whose loopback some servers trust.
func (u *upgradeStack) client(image string, args ...string) (string, error) {
	out, err := dockertest.CombinedOutput(append([]string{"run", "--rm", "--network", docker.NetworkName(u.stack), image}, args...)...)
	return strings.TrimSpace(string(out)), err
}

// containerID is the standing container's id: a release that finds nothing
// moved keeps it.
func (u *upgradeStack) containerID() string {
	out, _ := dockertest.Output("inspect", "-f", "{{.Id}}", docker.ServiceName(u.stack, u.name))
	return strings.TrimSpace(string(out))
}

// specs are rc.5's spec and the deployment's spec now, with its generated
// credentials, and those credentials.
func upgradeSpecs(t *testing.T, capability string) (old, current deploy.ServiceSpec, generated map[string]string, kind *Kind) {
	t.Helper()
	oldKind, kind := upgradeKinds(t, capability)
	generated, err := kind.Generated(t.Context(), testCredentials(t))
	if err != nil {
		t.Fatal(err)
	}
	return oldKind.ResolveLocal(nil, "", nil).Spec(), kind.ResolveLocal(nil, "", generated).Spec(), generated, kind
}

func TestIntegrationAStandingPostgresTakesItsDeploymentsOwnPassword(t *testing.T) {
	old, current, generated, _ := upgradeSpecs(t, "postgres")
	if old.Env["POSTGRES_PASSWORD"] != "app" || current.Env["POSTGRES_PASSWORD"] != generated["password"] || len(generated["password"]) != 64 {
		t.Fatalf("the specs: rc.5 %q, now %q", old.Env["POSTGRES_PASSWORD"], current.Env["POSTGRES_PASSWORD"])
	}
	stack, dk, own, psql := standingPostgres(t, "credpg")
	u := &upgradeStack{t: t, stack: stack, name: "postgres", dk: dk, own: own}
	u.ensure(old)
	u.healthy()
	// sql-guarantee: the app's own rows on a stand-in server, not the forge's database.
	if out, err := psql("app", "app", "app", "create table loaves (id serial primary key, name text); insert into loaves (name) select 'loaf ' || g from generate_series(1, 20) g"); err != nil {
		t.Fatalf("the rc.5 Postgres: %v %s", err, out)
	}

	u.ensure(current)
	u.healthy()
	password := generated["password"]
	if out, err := psql("app", password, "app", "select count(*) from loaves"); err != nil || out != "20" {
		t.Fatalf("the rows with the deployment's own password: %q %v", out, err)
	}
	if out, err := psql("app", "app", "app", "select 1"); err == nil || !strings.Contains(out, "password authentication failed") {
		t.Fatalf("the catalogue's password still opens the database: %q %v", out, err)
	}
	before := u.containerID()
	u.ensure(current)
	if u.containerID() != before {
		t.Fatal("a release with the same password remade the Postgres")
	}
}

func TestIntegrationAStandingMariaDBTakesItsDeploymentsOwnPassword(t *testing.T) {
	old, current, generated, kind := upgradeSpecs(t, "mariadb")
	if old.Env["MARIADB_PASSWORD"] != "app" || current.Env["MARIADB_PASSWORD"] != generated["password"] {
		t.Fatalf("the specs: rc.5 %q, now %q", old.Env["MARIADB_PASSWORD"], current.Env["MARIADB_PASSWORD"])
	}
	u := newUpgradeStack(t, "mariadb", kind.Local.Data)
	image := strings.ReplaceAll(kind.Local.Image, "{version}", kind.Defaults["version"])
	sql := func(password, statement string) (string, error) {
		return u.client(image, "mariadb", "-h", "mariadb", "-u", "app", "-p"+password, "app", "-N", "-e", statement)
	}
	u.ensure(old)
	u.healthy()
	// sql-guarantee: the application's own database, inside its container, not the forge's.
	if out, err := sql("app", "create table loaves (id int primary key); insert into loaves values (1), (2), (3)"); err != nil {
		t.Fatalf("the rc.5 MariaDB: %v %s", err, out)
	}

	u.ensure(current)
	u.healthy()
	password := generated["password"]
	if out, err := sql(password, "select count(*) from loaves"); err != nil || out != "3" {
		t.Fatalf("the rows with the deployment's own password: %q %v", out, err)
	}
	if out, err := sql("app", "select 1"); err == nil || !strings.Contains(out, "Access denied") {
		t.Fatalf("the catalogue's password still opens the database: %q %v", out, err)
	}
	// The app's user can still do everything it could in its database.
	if out, err := sql(password, "create table crumbs (id int); drop table crumbs"); err != nil {
		t.Fatalf("the app's rights in its database after the remake: %v %s", err, out)
	}
	before := u.containerID()
	u.ensure(current)
	if u.containerID() != before {
		t.Fatal("a release with the same password remade the MariaDB")
	}
}

func TestIntegrationAStandingRedisTakesItsDeploymentsOwnPassword(t *testing.T) {
	old, current, generated, kind := upgradeSpecs(t, "redis")
	if len(old.Env) != 0 || current.Env["REDISCLI_AUTH"] != generated["password"] {
		t.Fatalf("the specs: rc.5 %v, now %v", old.Env, current.Env)
	}
	u := newUpgradeStack(t, "redis", kind.Local.Data)
	image := strings.ReplaceAll(kind.Local.Image, "{version}", kind.Defaults["version"])
	cli := func(password string, args ...string) (string, error) {
		base := []string{"redis-cli", "-h", "redis", "--no-auth-warning"}
		if password != "" {
			base = append(base, "-a", password)
		}
		return u.client(image, append(base, args...)...)
	}
	u.ensure(old)
	u.healthy()
	// Written and never saved: only a clean stop keeps it (the remake stops
	// the service before it removes it).
	if out, err := cli("", "set", "loaf", "sourdough"); err != nil || out != "OK" {
		t.Fatalf("the rc.5 Redis: %q %v", out, err)
	}

	u.ensure(current)
	u.healthy()
	password := generated["password"]
	if out, err := cli(password, "get", "loaf"); err != nil || out != "sourdough" {
		t.Fatalf("the key with the deployment's own password: %q %v", out, err)
	}
	for _, wrong := range []string{"", "forge-redis"} {
		if out, _ := cli(wrong, "get", "loaf"); strings.Contains(out, "sourdough") || !strings.Contains(out, "NOAUTH") && !strings.Contains(out, "WRONGPASS") {
			t.Fatalf("redis answered with the password %q: %q", wrong, out)
		}
	}
	if url := kind.ResolveLocal(nil, "", generated).Inject["REDIS_URL"]; url != "redis://:"+password+"@redis:6379" {
		t.Fatalf("the app's REDIS_URL: %q", url)
	}
	before := u.containerID()
	u.ensure(current)
	if u.containerID() != before {
		t.Fatal("a release with the same password remade the Redis")
	}
}

func TestIntegrationAStandingObjectStoreTakesItsDeploymentsOwnKeys(t *testing.T) {
	old, current, generated, kind := upgradeSpecs(t, "s3")
	if old.Env["APP_ACCESS_KEY_ID"] != "local" || current.Env["APP_ACCESS_KEY_ID"] != generated["user"] || current.Env["ROOT_ACCESS_KEY_ID"] != generated["root_key"] {
		t.Fatalf("the specs: rc.5 %v, now %v", old.Env, current.Env)
	}
	u := newUpgradeStack(t, "s3", kind.Local.Data)
	s3 := func(key, secret string, args ...string) (string, error) {
		return u.client("curlimages/curl:8.22.0", append([]string{"-sS", "-f", "--aws-sigv4", "aws:amz:us-east-1:s3", "--user", key + ":" + secret}, args...)...)
	}
	u.ensure(old)
	u.healthy()
	if out, err := s3("local", "localsecret", "-X", "PUT", "--data-binary", "kept object", "http://s3:9000/app/notes/one.txt"); err != nil {
		t.Fatalf("the rc.5 store: %v %s", err, out)
	}

	u.ensure(current)
	u.healthy()
	if out, err := s3(generated["user"], generated["password"], "http://s3:9000/app/notes/one.txt"); err != nil || out != "kept object" {
		t.Fatalf("the object with the deployment's own key: %q %v", out, err)
	}
	for _, old := range [][2]string{{"local", "localsecret"}, {"forge-s3-root", "forge-s3-root-secret"}} {
		if out, err := s3(old[0], old[1], "http://s3:9000/app/notes/one.txt"); err == nil || strings.Contains(out, "kept object") {
			t.Fatalf("the rc.5 key %s still reads the bucket: %q", old[0], out)
		}
	}
	// The gateway's root key is the forge's: never given to the app.
	for name, value := range kind.ResolveLocal(nil, "", generated).Inject {
		if strings.Contains(value, generated["root_key"]) || strings.Contains(value, generated["root_secret"]) {
			t.Fatalf("the app is given the gateway's root key in %s", name)
		}
	}
	before := u.containerID()
	u.ensure(current)
	if u.containerID() != before {
		t.Fatal("a release with the same keys remade the store")
	}
}
