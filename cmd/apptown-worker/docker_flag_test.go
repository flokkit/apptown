package main

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/execfile"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/poll"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// The public -docker option must select the binary for a chat's saves too.
// The chosen fake returns Docker's exit 3 (nothing new); PATH deliberately
// contains no default docker. This uses the production worker executable.
func TestAWorkerChatSaveUsesTheConfiguredDocker(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "apptown-worker")
	if build := (execution.Runner{}).Run(context.Background(), execution.Local, execution.Request{
		Argv: []string{"go", "build", "-p", "2", "-o", binary, "flokkit.io/forge/cmd/apptown-worker"}, Timeout: 10 * time.Minute,
	}); build.RunErr != nil {
		t.Fatalf("build: %v %s", build.RunErr, build.Output)
	}
	fake := filepath.Join(dir, "chosen-docker")
	script := "#!/bin/sh\ncase \"$1\" in\ninspect) echo 'Error: No such object: absent'; exit 1;;\nrm|ps) exit 0;;\nrun) exit 3;;\n*) echo unexpected >&2; exit 1;;\nesac\n"
	if err := execfile.Write(fake, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	ca := workertest.NewCA(t)
	caFile, cert, key := ca.ServerFiles(t, dir)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	state := filepath.Join(dir, "state")
	cmd := exec.Command(binary, "-listen", address, "-installation", workertest.Installation, "-state", state, "-cert", cert, "-key", key, "-client-ca", caFile, "-agent-context=", "-docker", fake)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PATH=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+dir)
	log, err := os.Create(filepath.Join(dir, "worker.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = log.Close() })
	client := worker.SessionClient{URL: "https://" + address, Client: workertest.Client(t, ca, workertest.Installation)}
	// Generous: the worker starts beside a loaded gate.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if !poll.Until(ctx, time.Minute, 20*time.Millisecond, func() bool { _, err := client.List(ctx); return err == nil }) {
		t.Fatal("the worker never answered")
	}
	const id = "customdocker"
	if _, err := client.Put(ctx, id, worker.SessionSpec{Image: "test-image:latest", AgentSession: id}); err != nil {
		t.Fatal(err)
	}
	// A session's files normally come from uploads before it starts; this
	// probe needs only the spool directory, as Docker itself is the fake.
	if err := os.MkdirAll(filepath.Join(state, "chats", id), 0755); err != nil {
		t.Fatal(err)
	}
	tip, err := client.Save(ctx, id, strings.Repeat("a", 40), "", filepath.Join(dir, "save.bundle"))
	if err != nil || tip != "" {
		t.Fatalf("save bypassed the configured Docker binary: tip=%q err=%v", tip, err)
	}
}
