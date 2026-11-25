//go:build integration

package docker

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
)

func TestIntegrationBoundDatabaseSurvivesContainerLoss(t *testing.T) {
	dk := Scope{Client: CLI{Binary: "docker"}}
	stack := fmt.Sprintf("storage-binding-%d", time.Now().UnixNano())
	svc := deploy.ServiceSpec{Name: "postgres", Image: "postgres:16-alpine", KeepsImage: true, ConfigAtStart: true, Env: map[string]string{"POSTGRES_PASSWORD": "storage-test", "POSTGRES_DB": "app"}, Health: "pg_isready -U postgres -d app"}
	cname, volume := Current.Service(stack, svc.Name), Current.NeedVolume(stack, svc.Name)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := dk.Remove(ctx, cname); err != nil {
			t.Errorf("cleanup container: %v", err)
		}
		if err := dk.RemoveVolume(ctx, volume); err != nil {
			t.Errorf("cleanup volume: %v", err)
		}
		if err := dk.RemoveNetwork(ctx, Current.Network(stack)); err != nil {
			t.Errorf("cleanup network: %v", err)
		}
	})
	m := &bindingMemory{}
	own := Ownership{storage: m, Needs: map[string]string{svc.Name: "/var/lib/postgresql/data"}}
	boot := func() {
		t.Helper()
		if _, err := EnsureStandingServices(t.Context(), dk, stack, []deploy.ServiceSpec{svc}, "production", own); err != nil {
			t.Fatal(err)
		}
		if _, err := waitStackHealthy(t.Context(), dk, []deploy.ServiceSpec{svc}, stack); err != nil {
			t.Fatal(err)
		}
	}
	boot()
	if !m.found || m.binding.ImageID == svc.Image {
		t.Fatal("engine was not established immutably")
	}
	// sql-guarantee: this disposable external app table has no AppTown owner; seeded bytes surviving container loss are the guarantee.
	if out, err := dk.Run(t.Context(), "exec", cname, "psql", "-U", "postgres", "-d", "app", "-c", "CREATE TABLE kept(value text); INSERT INTO kept VALUES ('survived');"); err != nil {
		t.Fatalf("seed: %v: %s", err, out)
	}
	if _, err := dk.Run(t.Context(), "stop", cname); err != nil {
		t.Fatal(err)
	}
	if err := dk.Remove(t.Context(), cname); err != nil {
		t.Fatal(err)
	}
	changed := svc
	changed.Image = "postgres:17-alpine"
	if _, err := EnsureStandingServices(t.Context(), dk, stack, []deploy.ServiceSpec{changed}, "production", own); err == nil {
		t.Fatal("changed engine admitted to retained data")
	}
	if exists, err := dk.Exists(t.Context(), "inspect", cname); err != nil || exists {
		t.Fatalf("refusal started a container: exists=%t err=%v", exists, err)
	}
	boot()
	out, err := dk.Run(t.Context(), "exec", cname, "psql", "-U", "postgres", "-d", "app", "-Atc", "SELECT value FROM kept")
	if err != nil || strings.TrimSpace(string(out)) != "survived" {
		t.Fatalf("restored engine lost seeded data: %v: %s", err, out)
	}
}

// Published builds remain fixed even after their tags move to newer packaging.
const postgres1615 = "postgres:16.15-alpine@sha256:aa6cb33cf4c19c765cefcf48d96176258800870a9a1e930ad126200f9d825139"
const postgres1616 = "postgres:16.16-alpine@sha256:f2f98726820d0342055351c6e9d36e705f5bfffb3dd53ff556325114b08afb6d"

// The same volume survives a real engine move. Every engine is bound before
// first start; a reversible need can return after a writing-but-unhealthy start.
func TestIntegrationPostgresMovesForwardOnItsBoundVolume(t *testing.T) {
	for _, reversible := range []bool{false, true} {
		for _, failHealth := range []bool{false, true} {
			t.Run(fmt.Sprintf("reversible_%t/health_failure_%t", reversible, failHealth), func(t *testing.T) {
				stack := fmt.Sprintf("need-move-%d", time.Now().UnixNano())
				m := &bindingMemory{}
				newStarts, oldStarts := 0, 0
				moved := false
				cli := CLI{Binary: "docker"}
				dk := Scope{Client: &fakeClient{reply: func(req Command) ([]byte, error) {
					if len(req.Args) > 0 && (req.Args[0] == "run" || req.Args[0] == "start") {
						line := strings.Join(req.Args, " ")
						if strings.Contains(line, postgres1616) {
							newStarts++
							if m.binding.DeclaredImage != postgres1616 {
								t.Fatal("new engine first started before binding moved")
							}
						}
						if moved && strings.Contains(line, postgres1615) {
							oldStarts++
						}
					}
					return cli.Run(t.Context(), "", req)
				}}}
				svc := deploy.ServiceSpec{Name: "postgres", Image: postgres1615, Line: "postgres:16-alpine", Reversible: reversible, KeepsImage: true, ConfigAtStart: true, Env: map[string]string{"POSTGRES_PASSWORD": "storage-test", "POSTGRES_DB": "app"}, Health: "pg_isready -h 127.0.0.1 -U postgres -d app"}
				cname, volume := Current.Service(stack, svc.Name), Current.NeedVolume(stack, svc.Name)
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					clean := Scope{Client: cli}
					if err := clean.Remove(ctx, cname); err != nil {
						t.Error(err)
					}
					if err := clean.RemoveVolume(ctx, volume); err != nil {
						t.Error(err)
					}
					if err := clean.RemoveNetwork(ctx, Current.Network(stack)); err != nil {
						t.Error(err)
					}
				})
				own := Ownership{storage: m, Needs: map[string]string{svc.Name: "/var/lib/postgresql/data"}}
				start := func(spec deploy.ServiceSpec) {
					t.Helper()
					if _, err := EnsureStandingServices(t.Context(), dk, stack, []deploy.ServiceSpec{spec}, "production", own); err != nil {
						t.Fatal(err)
					}
				}
				start(svc)
				if _, err := waitStackHealthy(t.Context(), dk, []deploy.ServiceSpec{svc}, stack); err != nil {
					t.Fatal(err)
				}
				// sql-guarantee: external app rows are the data preserved through the engine move.
				sql := func(query string) string {
					t.Helper()
					out, err := dk.Run(t.Context(), "exec", cname, "psql", "-U", "postgres", "-d", "app", "-Atc", query)
					if err != nil {
						t.Fatalf("SQL: %v %s", err, out)
					}
					return strings.TrimSpace(string(out))
				}
				// The external app's rows: the preserved one and the newer engine's write.
				survived := func() string { return sql("SELECT value FROM kept WHERE value='survived'") }
				newerWrote := func() string { return sql("SELECT count(*) FROM kept WHERE value='new engine wrote'") }
				// sql-guarantee: external application rows are the engine move's preservation evidence.
				sql("CREATE TABLE kept(value text); INSERT INTO kept VALUES ('survived')")
				oldVolume := m.binding.VolumeCreated
				newer := svc
				newer.Image = postgres1616
				if failHealth {
					// sql-guarantee: the external database writes before its injected health failure.
					newer.Health = "psql -U postgres -d app -c \"INSERT INTO kept VALUES ('new engine wrote')\" && exit 1"
				}
				start(newer)
				moved = true
				if m.binding.DeclaredImage != postgres1616 || !m.binding.VolumeCreated.Equal(oldVolume) || newStarts != 1 {
					t.Fatalf("move: %+v starts=%d", m.binding, newStarts)
				}
				if got := dk.Text(t.Context(), "inspect", "-f", "{{.Config.Image}}", cname); got != postgres1616 {
					t.Fatalf("container image %q", got)
				}
				if failHealth {
					ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
					defer cancel()
					if _, err := waitStackHealthy(ctx, dk, []deploy.ServiceSpec{newer}, stack); err == nil {
						t.Fatal("unhealthy new engine passed")
					}
					if got := newerWrote(); got == "0" {
						t.Fatal("test engine never wrote before failing health")
					}
				} else if _, err := waitStackHealthy(t.Context(), dk, []deploy.ServiceSpec{newer}, stack); err != nil {
					t.Fatal(err)
				}
				if !failHealth {
					// sql-guarantee: the healthy newer external engine also writes before the return.
					sql("INSERT INTO kept VALUES ('new engine wrote')")
				}
				if got := survived(); got != "survived" {
					t.Fatalf("lost rows %q", got)
				}
				id := dk.Text(t.Context(), "inspect", "-f", "{{.Id}}", cname)
				var lines []string
				own.log = func(f string, args ...any) { lines = append(lines, fmt.Sprintf(f, args...)) }
				start(svc)
				if reversible {
					if m.binding.DeclaredImage != postgres1615 || oldStarts != 1 || !m.binding.VolumeCreated.Equal(oldVolume) {
						t.Fatalf("earlier engine did not return on its volume: %+v starts=%d", m.binding, oldStarts)
					}
					if _, err := waitStackHealthy(t.Context(), dk, []deploy.ServiceSpec{svc}, stack); err != nil {
						t.Fatal(err)
					}
					if got := dk.Text(t.Context(), "inspect", "-f", "{{.Config.Image}}", cname); got != postgres1615 {
						t.Fatalf("returned container image %q", got)
					}
					// sql-guarantee: the external app row survives the return on the same volume.
					if got := survived(); got != "survived" {
						t.Fatalf("return lost rows %q", got)
					}
					// sql-guarantee: writes from the newer external engine also survive its return.
					if newerWrote() == "0" {
						t.Fatal("return lost newer engine writes")
					}
					if !strings.Contains(strings.Join(lines, "\n"), "postgres moves from 16.16 to 16.15 on its volume") {
						t.Fatalf("missing return log: %v", lines)
					}
				} else {
					if m.binding.DeclaredImage != postgres1616 || oldStarts != 0 {
						t.Fatal("older engine returned without reversibility")
					}
					if got := dk.Text(t.Context(), "inspect", "-f", "{{.Id}}", cname); got != id {
						t.Fatal("older bundle needlessly recreated newer engine")
					}
					if !strings.Contains(strings.Join(lines, "\n"), "postgres stays on 16.16; this release names 16.15") {
						t.Fatalf("missing kept-build log: %v", lines)
					}
				}
				for _, image := range []string{"postgres:17-alpine", "postgres:16.17-bookworm", "other/postgres:16.17-alpine"} {
					bad := svc
					bad.Image = image
					if _, err := EnsureStandingServices(t.Context(), dk, stack, []deploy.ServiceSpec{bad}, "production", own); err == nil {
						t.Fatalf("off-line declaration admitted: %s", image)
					}
				}
			})
		}
	}
}
