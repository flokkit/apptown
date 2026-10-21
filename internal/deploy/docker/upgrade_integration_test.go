//go:build integration

package docker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/poll"
)

// A standing deployment made before needs had named volumes keeps its
// database: its postgres container (data in Docker's anonymous volume) is
// reused as it is by this boot, so a row written before the upgrade is
// there after it, no named volume is made and nothing new is recorded as
// the need's data. UPGRADE_STACK names a stack another build (main) booted;
// unset, the test boots one the way main did (no data mount).
func TestIntegrationAnUpgradeKeepsAnExistingDatabase(t *testing.T) {
	dk := Scope{Client: CLI{Binary: "docker"}}
	svc := deploy.ServiceSpec{Name: "postgres", Image: "postgres:16-alpine", Health: "pg_isready -U app -d app",
		Env: map[string]string{"POSTGRES_USER": "app", "POSTGRES_PASSWORD": "app", "POSTGRES_DB": "app"}}
	stack := os.Getenv("UPGRADE_STACK")
	cname := Current.Service(stack, "postgres")
	psql := func(sql string) string {
		t.Helper()
		var out []byte
		var err error
		if poll.Until(context.Background(), 30*time.Second, 300*time.Millisecond, func() bool {
			out, err = exec.Command("docker", "exec", cname, "psql", "-U", "app", "-d", "app", "-tAc", sql).CombinedOutput()
			return err == nil
		}) {
			return strings.TrimSpace(string(out))
		}
		t.Fatalf("psql %q: %v %s", sql, err, out)
		return ""
	}
	if stack == "" {
		stack = fmt.Sprintf("env-itupg%d-main", time.Now().UnixNano()%1_000_000)
		cname = Current.Service(stack, "postgres")
		// The boot before named volumes: no Needs, so no data mount.
		if _, err := EnsureStandingServices(context.Background(), dk, stack, []deploy.ServiceSpec{svc}, "production", Ownership{}); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", "-v", cname).Run()
		_ = exec.Command("docker", "network", "rm", Current.Network(stack)).Run()
		_ = exec.Command("docker", "volume", "rm", Current.NeedVolume(stack, "postgres")).Run()
	})
	// sql-guarantee: the application's own database, inside its container, not AppTown's.
	psql("CREATE TABLE IF NOT EXISTS kept (v text); INSERT INTO kept VALUES ('before the upgrade')")
	before := dk.Text(context.Background(), "inspect", "-f", "{{.Id}}", cname)

	var owned []deploy.Owned
	own := Ownership{Record: func(o deploy.Owned) error { owned = append(owned, o); return nil }, Needs: map[string]string{"postgres": "/var/lib/postgresql/data"}}
	if _, err := EnsureStandingServices(context.Background(), dk, stack, []deploy.ServiceSpec{svc}, "production", own); err != nil {
		t.Fatal(err)
	}
	if after := dk.Text(context.Background(), "inspect", "-f", "{{.Id}}", cname); after != before || before == "" {
		t.Fatalf("the database container was replaced: %s -> %s", before, after)
	}
	if got := psql("TABLE kept"); got != "before the upgrade" {
		t.Fatalf("the row is gone after the upgrade: %q", got)
	}
	if dk.OK(context.Background(), "volume", "inspect", Current.NeedVolume(stack, "postgres")) {
		t.Fatal("an empty named volume was made beside the existing database")
	}
	if slices.ContainsFunc(owned, func(o deploy.Owned) bool { return o.Kind == "volume" || o.Kind == "container" }) {
		t.Fatalf("the reused database was recorded as made by this boot: %+v", owned)
	}
}

// The 21 Oct incident, on a real daemon: a postgres service made before
// needs had named volumes (data in its image's anonymous volume) whose
// declared parameters moved is made again on that same volume, so a row
// written before is there after, and no empty named volume is made.
func TestIntegrationANeedMadeAgainKeepsItsAnonymousVolume(t *testing.T) {
	dk := Scope{Client: CLI{Binary: "docker"}}
	stack := fmt.Sprintf("env-itcarry%d-main", time.Now().UnixNano()%1_000_000)
	cname := Current.Service(stack, "postgres")
	svc := deploy.ServiceSpec{Name: "postgres", Image: "postgres:16-alpine", Health: "pg_isready -U app -d app", ConfigAtStart: true,
		Env: map[string]string{"POSTGRES_USER": "app", "POSTGRES_PASSWORD": "old", "POSTGRES_DB": "app"}}
	var anonymous string
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", cname).Run()
		_ = exec.Command("docker", "network", "rm", Current.Network(stack)).Run()
		_ = exec.Command("docker", "volume", "rm", Current.NeedVolume(stack, "postgres")).Run()
		if anonymous != "" {
			_ = exec.Command("docker", "volume", "rm", anonymous).Run()
		}
	})
	psql := func(sql string) string {
		t.Helper()
		var out []byte
		var err error
		if poll.Until(context.Background(), 60*time.Second, 300*time.Millisecond, func() bool {
			out, err = exec.Command("docker", "exec", cname, "psql", "-U", "app", "-d", "app", "-tAc", sql).CombinedOutput()
			return err == nil
		}) {
			return strings.TrimSpace(string(out))
		}
		t.Fatalf("psql %q: %v %s", sql, err, out)
		return ""
	}
	// The boot before named volumes: no Needs, so no data mount.
	if _, err := EnsureStandingServices(context.Background(), dk, stack, []deploy.ServiceSpec{svc}, "production", Ownership{}); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: the application's own database, inside its container, not AppTown's.
	psql("CREATE TABLE kept (v text); INSERT INTO kept VALUES ('before the remake')")
	m, err := DataMountAt(context.Background(), dk, cname, "/var/lib/postgresql/data")
	if err != nil || m.Volume == "" {
		t.Fatalf("the old service's data mount: %+v %v", m, err)
	}
	anonymous = m.Volume
	before := dk.Text(context.Background(), "inspect", "-f", "{{.Id}}", cname)

	svc.Env = map[string]string{"POSTGRES_USER": "app", "POSTGRES_PASSWORD": "new", "POSTGRES_DB": "app"}
	own := Ownership{Needs: map[string]string{"postgres": "/var/lib/postgresql/data"}}
	if _, err := EnsureStandingServices(context.Background(), dk, stack, []deploy.ServiceSpec{svc}, "production", own); err != nil {
		t.Fatal(err)
	}
	if after := dk.Text(context.Background(), "inspect", "-f", "{{.Id}}", cname); after == before {
		t.Fatal("the service was not made again; the test proves nothing")
	}
	if got := psql("TABLE kept"); got != "before the remake" {
		t.Fatalf("the row is gone after the remake: %q", got)
	}
	if dk.OK(context.Background(), "volume", "inspect", Current.NeedVolume(stack, "postgres")) {
		t.Fatal("an empty named volume was made beside the data")
	}
}
