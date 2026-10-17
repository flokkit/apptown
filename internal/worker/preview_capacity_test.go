package worker_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// A worker refuses a preview larger than its own daemon can give a
// container before it removes the earlier one: the running preview
// stays, and nothing is started.
func TestAWorkerRefusesAnOversizedPreviewBeforeRemovingTheRunningOne(t *testing.T) {
	binary, dk := sweepDocker(t)
	binary.reply = func(args []string) ([]byte, error, bool) {
		if len(args) > 2 && args[0] == "info" && strings.Contains(args[2], "MemTotal") {
			return []byte("8589934592\t8\t/var/lib/docker\n"), nil, true
		}
		return nil, nil, false
	}
	ca := workertest.NewCA(t)
	srv := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Previews: binary}
	hs := workertest.StartServer(t, ca, srv)
	previews := worker.Previews{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	name := "forge-preview-change-44"
	if err := os.WriteFile(filepath.Join(dk, "running-"+name), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := appcontract.Parse([]byte("version: 1\napp:\n  web:\n    static: .\n    public: true\n    resources: { memory: 16G }\n"))
	if err != nil {
		t.Fatal(err)
	}
	// The app fits; an independently supplied workflow sidecar does not.
	c.Parts[0].Resources.MemoryBytes = 64 << 20
	services := []deploy.ServiceSpec{{Name: "sidecar", Image: "redis:7", Resources: appcontract.Resources{MemoryBytes: 16 << 30}}}
	_, err = previews.Boot(context.Background(), worker.PreviewSpec{Name: name, StackID: "pv44", Slug: "app", Incarnation: "new", ProbeSeconds: 1}, docker.Prepared{Version: docker.PreparedVersion, Contract: c, Services: services}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "declares 16G of memory; this Docker host has 8G") {
		t.Fatalf("an oversized preview: %v", err)
	}
	for _, call := range binary.calls {
		if strings.HasPrefix(call, "rm ") || strings.HasPrefix(call, "run ") || strings.HasPrefix(call, "stop ") {
			t.Fatalf("the worker touched the running preview first: %s", call)
		}
	}
	if !exists(filepath.Join(dk, "running-"+name)) {
		t.Fatal("the running preview is gone")
	}
}

// The forge asks the worker before any effect whether its daemon can hold
// a contract: a refusal is the worker's sentence, typed as a refusal, and
// the asking changes nothing there.
func TestAWorkerAnswersWhetherItsDaemonCanHoldAPreview(t *testing.T) {
	binary, _ := sweepDocker(t)
	binary.reply = func(args []string) ([]byte, error, bool) {
		if len(args) > 2 && args[0] == "info" && strings.Contains(args[2], "MemTotal") {
			return []byte("8589934592\t8\t/var/lib/docker\n"), nil, true
		}
		return nil, nil, false
	}
	ca := workertest.NewCA(t)
	srv := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Previews: binary}
	hs := workertest.StartServer(t, ca, srv)
	previews := worker.Previews{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	parse := func(y string) *appcontract.Contract {
		c, err := appcontract.Parse([]byte(y))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if err := previews.Admit(t.Context(), parse("version: 1\napp:\n  web:\n    static: .\n    public: true\n    resources: { memory: 1G }\n")); err != nil {
		t.Fatalf("a preview the worker can hold: %v", err)
	}
	err := previews.Admit(t.Context(), parse("version: 1\napp:\n  web:\n    static: .\n    public: true\n    resources: { memory: 16G }\n"))
	var refused *worker.AdmissionRefused
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "declares 16G of memory; this Docker host has 8G") {
		t.Fatalf("an oversized preview: %v", err)
	}
	for _, call := range binary.calls {
		if !strings.HasPrefix(call, "info ") {
			t.Fatalf("asking did %q on the worker", call)
		}
	}
}
