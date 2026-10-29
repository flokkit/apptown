package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/ids"
)

type noExtensions struct{}

func (noExtensions) EnabledExtensions(context.Context, ids.ID) ([]string, error) { return nil, nil }

func readSettings(t *testing.T, dir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPiAgentSurfaceWithoutBakedExtension(t *testing.T) {
	t.Parallel()
	daemon := dockerfake.New()
	daemon.SetReply(func([]string) ([]byte, error, bool) { return nil, nil, true })
	scope := docker.Scope{Client: daemon}
	home := NewPreparer(publicConfig{}, selectedSlots{}, noExtensions{}, &scope, "test-agent")
	dir := t.TempDir()
	if err := home.StageAgentDir(t.Context(), dir, ids.Nil); err != nil {
		t.Fatal(err)
	}
	s := readSettings(t, dir)
	if _, ok := s["packages"]; ok {
		t.Fatal("no baked extension → no package entry (a session-start npm fetch is forbidden)")
	}
	if s["defaultProjectTrust"] != "never" {
		t.Fatal("trust stays closed even without delegation")
	}
	if _, err := os.Stat(filepath.Join(dir, "agents")); !os.IsNotExist(err) {
		t.Fatal("no extension → no agent roster (agents nobody can call are clutter)")
	}
	if home.Delegation(context.Background()) != "" {
		t.Fatal("steering must not promise a tool the image does not carry")
	}
}

// Pi finds its transcripts and steering where the caller says: in the run's
// folder on AppTown's host, elsewhere on a worker.
func TestStepArgsPointAtTheGivenPaths(t *testing.T) {
	t.Parallel()
	joined := strings.Join(StepArgs(FolderStepPaths, "step-1-a1", true, "", "", "go"), " ")
	if !strings.Contains(joined, "--session-dir .apptown/sessions --session-id step-1-a1") || !strings.Contains(joined, "--append-system-prompt .apptown/steering.md") {
		t.Fatalf("folder paths: %s", joined)
	}
	joined = strings.Join(StepArgs(StepPaths{SessionFile: "/apptown/state/sessions/x_step-1-a1.jsonl", Steering: "/apptown/in/steering.md"}, "step-1-a1", true, "", "", "go"), " ")
	if !strings.Contains(joined, "--session /apptown/state/sessions/x_step-1-a1.jsonl") || strings.Contains(joined, "--session-dir") || strings.Contains(joined, "--session-id") ||
		!strings.Contains(joined, "--append-system-prompt /apptown/in/steering.md") || strings.Contains(joined, ".apptown") {
		t.Fatalf("given paths: %s", joined)
	}
}
