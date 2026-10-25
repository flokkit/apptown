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
	own := Ownership{Storage: m, Needs: map[string]string{svc.Name: "/var/lib/postgresql/data"}}
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
