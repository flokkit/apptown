//go:build integration

package execution_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executiontest"
	"github.com/flokkit/apptown/stock"
)

// With TLS asked for in the environment, the real CLI's default endpoint is
// TCP with TLS; AppTown selects the same, never its local Unix socket, even
// when that socket works.
func TestIntegrationTheDefaultTLSDaemonIsNotTheLocalSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "review-only"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", dir)
	t.Setenv("DOCKER_CERT_PATH", dir)
	t.Setenv("DOCKER_CONTEXT", "default")
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_TLS", "")
	t.Setenv("DOCKER_TLS_VERIFY", "1")
	out, err := exec.CommandContext(ctx, "docker", "context", "inspect", "default", "--format", "{{.Endpoints.docker.Host}}").CombinedOutput()
	expected := strings.TrimSpace(string(out))
	if err != nil || expected != "tcp://localhost:2376" {
		t.Fatalf("CLI selection fixture: %q %v", out, err)
	}
	// This reproduction uses an unavailable configured endpoint and the real
	// local daemon; it never sends a create to the configured TCP endpoint.
	out, err = exec.CommandContext(ctx, "docker", "version").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "tcp://localhost:2376") {
		t.Fatalf("configured endpoint must be unavailable: %q %v", out, err)
	}
	selected := execution.SelectDocker()
	if selected.Refused != nil || selected.Host == expected {
		return
	}
	d := execution.DockerTasks{ToolVersions: stock.Versions(), Daemon: selected}
	taskKey := executiontest.UniqueKeys("round6")("default-tls")
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := d.Release(cleanup, execution.TaskRef{Key: taskKey}); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	obs, err := d.Ensure(ctx, dockerSpec(taskKey, "must not run", 0, false))
	if obs.ID != "" || err == nil {
		t.Fatalf("CLI selected %s, AppTown selected %s and created local task %s: %v", expected, selected.Host, obs.ID, err)
	}
	t.Fatalf("AppTown selected %s instead of CLI endpoint %s (create failed: %v)", selected.Host, expected, err)
}
