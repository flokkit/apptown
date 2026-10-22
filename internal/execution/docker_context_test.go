package execution_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execfile"

	"github.com/flokkit/apptown/internal/execution"
)

func writeDockerContext(t *testing.T, configDir, name, host string) {
	t.Helper()
	sum := sha256.Sum256([]byte(name))
	dir := filepath.Join(configDir, "contexts", "meta", hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"Name":"` + name + `","Endpoints":{"docker":{"Host":"` + host + `"}}}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// recordingDocker is a docker binary that records its arguments and the
// DOCKER_ variables it was given, one call per line, and answers nothing.
func recordingDocker(t *testing.T) (binary, calls string) {
	t.Helper()
	dir := t.TempDir()
	binary, calls = filepath.Join(dir, "docker"), filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$* | $(env | grep '^DOCKER_' | sort | tr '\\n' ' ')\" >> " + calls + "\nexit 1\n"
	if err := execfile.Write(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary, calls
}

func selectWith(t *testing.T, host, contextName, configDir string) execution.DockerSelection {
	t.Helper()
	for _, v := range []string{"DOCKER_TLS_VERIFY", "DOCKER_TLS", "DOCKER_CERT_PATH"} {
		if _, set := os.LookupEnv(v); !set {
			t.Setenv(v, "")
		}
	}
	t.Setenv("DOCKER_HOST", host)
	t.Setenv("DOCKER_CONTEXT", contextName)
	t.Setenv("DOCKER_CONFIG", configDir)
	return execution.SelectDocker()
}

// A context is resolved once, to its daemon's address, and every call passes
// that address itself: rewriting the context afterwards, or naming another
// context or host in the environment, does not move a call to another daemon.
func TestADockerCallIsPinnedToTheDaemonSelectedAtStartup(t *testing.T) {
	configDir := t.TempDir()
	writeDockerContext(t, configDir, "work", "tcp://first.invalid:2376")
	selected := selectWith(t, "", "work", configDir)
	if selected.Refused != nil || selected.Host != "tcp://first.invalid:2376" {
		t.Fatalf("selected %+v", selected)
	}
	writeDockerContext(t, configDir, "work", "tcp://second.invalid:2376")
	t.Setenv("DOCKER_HOST", "tcp://third.invalid:2376")
	binary, calls := recordingDocker(t)
	d := execution.DockerTasks{Binary: binary, Daemon: selected}
	_, _ = d.Observe(context.Background(), execution.TaskRef{Key: "k"})
	_ = d.Stop(context.Background(), execution.TaskRef{Key: "k"})
	body, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) < 2 {
		t.Fatalf("calls: %q", body)
	}
	for _, line := range lines {
		args, env, _ := strings.Cut(line, " | ")
		if !strings.HasPrefix(args, "--host tcp://first.invalid:2376 ") {
			t.Fatalf("a call not pinned to the selected daemon: %q", line)
		}
		if strings.Contains(env, "DOCKER_CONTEXT=") || strings.Contains(env, "DOCKER_HOST=") {
			t.Fatalf("a call given another selection in its environment: %q", line)
		}
	}
}

// A selection that cannot be pinned (a context that cannot be read, or one
// that needs TLS files) is refused for AppTown's lifetime: nothing is
// created and nothing is called, even once the context becomes readable,
// and its backend never matches a resolved one.
func TestAnUnpinnableDockerSelectionCreatesNothing(t *testing.T) {
	configDir := t.TempDir()
	missing := selectWith(t, "", "later", configDir)
	if missing.Refused == nil || !strings.Contains(missing.Backend(), "(unresolved)") {
		t.Fatalf("an unreadable context: %+v %q", missing, missing.Backend())
	}
	writeDockerContext(t, configDir, "tls", "tcp://tls.invalid:2376")
	sum := sha256.Sum256([]byte("tls"))
	if err := os.MkdirAll(filepath.Join(configDir, "contexts", "tls", hex.EncodeToString(sum[:]), "docker"), 0o700); err != nil {
		t.Fatal(err)
	}
	withTLS := selectWith(t, "", "tls", configDir)
	if withTLS.Refused == nil || !strings.Contains(withTLS.Refused.Error(), "DOCKER_HOST") {
		t.Fatalf("a context with TLS files: %+v", withTLS)
	}
	writeDockerContext(t, configDir, "later", "tcp://later.invalid:2376")
	binary, calls := recordingDocker(t)
	for _, selected := range []execution.DockerSelection{missing, withTLS} {
		d := execution.DockerTasks{Binary: binary, Daemon: selected}
		spec := execution.TaskSpec{Key: "k", Image: "busybox", Argv: []string{"true"}, Admission: execution.Admission{Profile: "local-docker", Expires: time.Now().Add(time.Hour)}}
		if _, err := d.Ensure(context.Background(), spec); !errors.Is(err, execution.ErrUnsupported) {
			t.Fatalf("creating on %q: %v", selected.Backend(), err)
		}
		if _, err := d.Observe(context.Background(), execution.TaskRef{Key: "k"}); !errors.Is(err, execution.ErrTaskUnobservable) {
			t.Fatalf("observing on %q: %v", selected.Backend(), err)
		}
	}
	if _, err := os.Stat(calls); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused selection called docker")
	}
	if resolved := selectWith(t, "", "later", configDir); resolved.Refused != nil || resolved.Backend() == missing.Backend() {
		t.Fatalf("the context once readable: %+v, backend %q", resolved, resolved.Backend())
	}
}

// DOCKER_HOST selects the daemon over any context; the default context is
// the default socket; and one daemon reached either way is one backend.
func TestADockerDaemonIsNamedByItsAddress(t *testing.T) {
	configDir := t.TempDir()
	writeDockerContext(t, configDir, "work", "tcp://daemon.invalid:2376")
	byHost := selectWith(t, "tcp://daemon.invalid:2376", "other", configDir)
	byContext := selectWith(t, "", "work", configDir)
	if byHost.Host != "tcp://daemon.invalid:2376" || byHost.Backend() != byContext.Backend() {
		t.Fatalf("by DOCKER_HOST %+v, by context %+v", byHost, byContext)
	}
	if def := selectWith(t, "", "default", configDir); def.Host != "unix:///var/run/docker.sock" || def.Refused != nil {
		t.Fatalf("the default context: %+v", def)
	}
}

// The default context with TLS asked for in the environment is the local TLS
// port, as the CLI selects it, never the local socket; and TLS settings in
// the environment reach a call only where the CLI would apply them
// (DOCKER_HOST and the default context), not a named context's daemon,
// which carries its own.
func TestDockerTLSSettingsFollowTheCLIsSelection(t *testing.T) {
	configDir := t.TempDir()
	plain := selectWith(t, "", "default", configDir)
	t.Setenv("DOCKER_TLS_VERIFY", "1")
	t.Setenv("DOCKER_CERT_PATH", "/certs")
	withTLS := selectWith(t, "", "default", configDir)
	if withTLS.Host != "tcp://localhost:2376" || withTLS.Refused != nil || withTLS.Backend() == plain.Backend() {
		t.Fatalf("the default context with TLS: %+v (without: %+v)", withTLS, plain)
	}
	writeDockerContext(t, configDir, "work", "tcp://work.invalid:2375")
	for _, tc := range []struct {
		name     string
		selected execution.DockerSelection
		tls      bool
	}{
		{"DOCKER_HOST", selectWith(t, "tcp://host.invalid:2376", "", configDir), true},
		{"the default context", withTLS, true},
		{"a named context", selectWith(t, "", "work", configDir), false},
	} {
		binary, calls := recordingDocker(t)
		_, _ = execution.DockerTasks{Binary: binary, Daemon: tc.selected}.Observe(context.Background(), execution.TaskRef{Key: "k"})
		body, err := os.ReadFile(calls)
		if err != nil {
			t.Fatal(err)
		}
		_, env, _ := strings.Cut(strings.TrimSpace(string(body)), " | ")
		if got := strings.Contains(env, "DOCKER_TLS_VERIFY=1") && strings.Contains(env, "DOCKER_CERT_PATH=/certs"); got != tc.tls {
			t.Fatalf("%s: TLS settings passed %v, want %v: %q", tc.name, got, tc.tls, env)
		}
	}
}
