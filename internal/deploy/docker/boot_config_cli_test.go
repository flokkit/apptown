package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execfile"
)

// With the real Docker CLI, a fake daemon and fake native credential
// helpers holding a machine login for the same registry: a boot's pulls send
// exactly the boot's own login, or none, and no helper is ever asked or
// told anything. Two projects' logins for one registry never meet, and a
// boot's configuration is gone when it ends. (Not parallel: it sets PATH.)
func TestABootsCredentialsAreItsOwnWithTheRealCLI(t *testing.T) {
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("the Docker CLI is not installed")
	}
	dir := t.TempDir()
	helperLog := filepath.Join(dir, "helpers.log")
	// Every native helper the CLI might pick answers with the machine's
	// login, and records that it was asked.
	helper := "#!/bin/sh\necho \"$0 $1\" >> \"" + helperLog + "\"\ncat >> \"" + helperLog + "\"\n" +
		"[ \"$1\" = get ] && echo '{\"ServerURL\":\"registry.review.invalid\",\"Username\":\"machine\",\"Secret\":\"machine-secret\"}'\nexit 0\n"
	for _, name := range []string{"osxkeychain", "secretservice", "pass", "desktop", "wincred"} {
		if err := execfile.Write(filepath.Join(dir, "docker-credential-"+name), []byte(helper), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "1.45")
	t.Setenv("DOCKER_CONFIG", filepath.Join(dir, "ambient"))

	var mu sync.Mutex
	var sent []string // each pull's X-Registry-Auth, decoded
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", "1.45")
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/images/create") {
			auth, _ := base64.URLEncoding.DecodeString(r.Header.Get("X-Registry-Auth"))
			mu.Lock()
			sent = append(sent, string(auth))
			mu.Unlock()
			_, _ = w.Write([]byte(`{"status":"Pulled"}` + "\n"))
			return
		}
		_, _ = w.Write([]byte(`{"ApiVersion":"1.45"}`))
	}))
	defer daemon.Close()
	machine := Scope{Client: CLI{Binary: binary}, Host: "tcp://" + strings.TrimPrefix(daemon.URL, "http://")}

	pull := func(login *RegistryCredential) (config string) {
		t.Helper()
		dk, done, err := machine.WithBootConfig(t.Context(), filepath.Join(dir, "boots"), login)
		if err != nil {
			t.Fatal(err)
		}
		if out, err := dk.Command(context.Background(), Command{Args: []string{"pull", "registry.review.invalid/app:1"}}); err != nil {
			t.Fatalf("pull: %v: %s", err, out)
		}
		if err := done(); err != nil {
			t.Fatal(err)
		}
		return dk.Config
	}
	first := pull(&RegistryCredential{Host: "registry.review.invalid", Username: "project-a", Token: "token-a"})
	second := pull(&RegistryCredential{Host: "registry.review.invalid", Username: "project-b", Token: "token-b"})
	pull(nil)

	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 3 {
		t.Fatalf("pulls: %q", sent)
	}
	var auth struct{ Username, Password string }
	for i, want := range []string{"project-a:token-a", "project-b:token-b", ":"} {
		auth.Username, auth.Password = "", ""
		_ = json.Unmarshal([]byte(sent[i]), &auth)
		if got := auth.Username + ":" + auth.Password; got != want {
			t.Errorf("pull %d sent %q, want %q (%s)", i+1, got, want, sent[i])
		}
	}
	if strings.Contains(strings.Join(sent, " "), "machine-secret") {
		t.Fatalf("a boot sent the machine's login: %q", sent)
	}
	if body, err := os.ReadFile(helperLog); err == nil && len(body) > 0 {
		t.Fatalf("a credential helper was used: %s", body)
	}
	for _, config := range []string{first, second} {
		if _, err := os.Stat(config); !os.IsNotExist(err) {
			t.Fatalf("a boot's configuration outlived it: %v", err)
		}
	}
}

// A boot's configuration is its own until the boot ends: another boot,
// however much later, never removes it. Its owner, starting again with no
// boot running, removes every one left by a stop or a failed removal.
func TestBootConfigurationsAreRemovedOnlyByTheirOwnerStarting(t *testing.T) {
	t.Parallel()
	scratch := t.TempDir()
	dk := Scope{Client: &recordingClient{}, Host: "unix:///nowhere"}
	running, _, err := dk.WithBootConfig(t.Context(), scratch, &RegistryCredential{Host: "r.example", Username: "u", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	long := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(running.Config, long, long); err != nil {
		t.Fatal(err)
	}
	if _, _, err := dk.WithBootConfig(t.Context(), scratch, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(running.Config, "config.json")); err != nil {
		t.Fatalf("another boot removed a running boot's configuration: %v", err)
	}
	if err := SweepBootConfigs(scratch); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(scratch, bootConfigPattern)); len(left) != 0 {
		t.Fatalf("after the owner's start: %q", left)
	}
	if _, _, err := dk.WithBootConfig(t.Context(), "", nil); err == nil {
		t.Fatal("a boot configuration was made outside the boot's own folder")
	}
}
