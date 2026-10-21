//go:build integration

package docker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/poll"
)

func TestIntegrationAFreshDatabaseGetsItsStartupBound(t *testing.T) {
	dk := Scope{Client: CLI{Binary: "docker"}}
	stack := fmt.Sprintf("review-slice2-health-%d", time.Now().UnixNano())
	svc := deploy.ServiceSpec{Name: "mariadb", Image: "mariadb:11.4.13", Health: "healthcheck.sh --connect --innodb_initialized", Command: []string{"--innodb-buffer-pool-size=64M", "--max-connections=20", "--performance-schema=OFF"}, Env: map[string]string{"MARIADB_DATABASE": "app", "MARIADB_USER": "app", "MARIADB_PASSWORD": "app", "MARIADB_RANDOM_ROOT_PASSWORD": "1", "MARIADB_ROOT_HOST": "localhost"}, Resources: appcontract.Resources{CPUs: 0.1, MemoryBytes: 268435456}}
	cname := Current.Service(stack, svc.Name)
	t.Cleanup(func() {
		_, _ = dk.Run(context.Background(), "rm", "-fv", cname)
		_, _ = dk.Run(context.Background(), "network", "rm", Current.Network(stack))
	})
	if _, err := EnsureStandingServices(t.Context(), dk, stack, []deploy.ServiceSpec{svc}, "production", Ownership{}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, gateErr := waitStackHealthy(t.Context(), dk, Current, []deploy.ServiceSpec{svc}, stack)
	t.Logf("startup gate after %s: %v", time.Since(start), gateErr)
	healthy := poll.Until(t.Context(), healthBound, time.Second, func() bool {
		return dk.Text(t.Context(), "inspect", "-f", "{{.State.Health.Status}}", cname) == "healthy"
	})
	if !healthy {
		t.Fatal("database never became healthy")
	}
	if gateErr != nil {
		t.Fatalf("healthy database rejected during ordinary first initialization (healthy after %s): %v", time.Since(start), gateErr)
	}
}
