package apptown

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/project"
	"os"
	"path/filepath"
	"strings"
	"testing"

	piharness "github.com/flokkit/apptown/internal/apptown/agentharness/pi"
	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// piSurface is the Pi preparer over the real model selection of db's slots,
// reading the agent image's labels from a fake Docker. It enables no
// extension, and has no model configuration: staging the agent directory
// writes no models.json.
func piSurface(db *database.DB, labels map[string]string) (*model.Slots, piharness.Preparer) {
	slots := model.New(db)
	return slots, piharness.NewPreparer(nil, model.NewSelection(slots), noExtensions{}, &docker.Scope{Client: fakeAgentLabels(labels)}, "apptown-agent")
}

type noExtensions struct{}

func (noExtensions) EnabledExtensions(context.Context, ids.ID) ([]string, error) { return nil, nil }

func TestPiAgentSurfaceWithBakedExtension(t *testing.T) {
	t.Parallel()
	slots, home := piSurface(testDB(t), map[string]string{"apptown.pi-subagents.version": "0.61.0"})
	// This test exercises the stock slot fallback policy.
	if err := slots.SeedFallbacks(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Instance slots: default and fast diverge, so worker and scout must too.
	for slot, pattern := range map[string]string{
		"default": "openrouter/openai/gpt-5.6-luna",
		"fast":    "openrouter/google/gemini-3.7-flash",
	} {
		if err := slots.Set(context.Background(), slot, "standard", pattern, "fixture"); err != nil {
			t.Fatal(err)
		}
	}

	dir := t.TempDir()
	if err := home.StageAgentDir(t.Context(), dir, ids.Nil); err != nil {
		t.Fatal(err)
	}

	s := readSettings(t, dir)
	if s["defaultProjectTrust"] != "never" {
		t.Fatalf("project trust must be closed, got %v", s["defaultProjectTrust"])
	}
	if sk, _ := s["skills"].([]any); len(sk) != 1 || sk[0] != "/work/.agents/skills" {
		t.Fatalf("the blessed repo skills path must be listed, got %v", s["skills"])
	}
	if pk, _ := s["packages"].([]any); len(pk) != 1 || pk[0] != "npm:pi-subagents@0.61.0" {
		t.Fatalf("the baked package must be pinned by version, got %v", s["packages"])
	}
	sub, _ := s["subagents"].(map[string]any)
	if sub == nil || sub["disableBuiltins"] != true {
		t.Fatalf("stock roster must be off (the generated roster is the roster), got %v", s["subagents"])
	}
	if sub["defaultModel"] != "openrouter/openai/gpt-5.6-luna" {
		t.Fatalf("subagent default model must be the default slot, got %v", sub["defaultModel"])
	}

	worker, err := os.ReadFile(filepath.Join(dir, "agents", "worker.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(worker), "model: openrouter/openai/gpt-5.6-luna") {
		t.Fatalf("worker must run on the default slot:\n%s", worker)
	}
	scout, err := os.ReadFile(filepath.Join(dir, "agents", "scout.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(scout), "model: openrouter/google/gemini-3.7-flash") {
		t.Fatalf("scout must run on the fast slot:\n%s", scout)
	}
	// Complaint #6: a lookup must never fail an acceptance contract, and
	// worker must never auto-require a reviewer agent this roster lacks.
	if !strings.Contains(string(scout), "level: none") || !strings.Contains(string(scout), "reason:") {
		t.Fatalf("scout acceptance must be explicitly none with a reason:\n%s", scout)
	}
	if !strings.Contains(string(worker), "acceptance: attested") {
		t.Fatalf("worker acceptance must be pinned to attested:\n%s", worker)
	}

	if target, err := os.Readlink(filepath.Join(dir, "npm")); err != nil || target != "/opt/pi-baked/.pi/agent/npm" {
		t.Fatalf("npm symlink must point at the baked package dir, got %q err %v", target, err)
	}

	// Re-staging (console re-provision) must not fail on the existing symlink.
	if err := home.StageAgentDir(t.Context(), dir, ids.Nil); err != nil {
		t.Fatalf("re-stage: %v", err)
	}
}

func TestPiAgentSurfaceProjectProfile(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	slots, home := piSurface(db, map[string]string{"apptown.pi-subagents.version": "0.61.0"})
	plainTestProject(t, db, "p", "P")
	if err := project.New(db, nil).SetModelProfile(t.Context(), idstest.N(1), "high"); err != nil {
		t.Fatal(err)
	}
	for profile, pattern := range map[string]string{"standard": "openrouter/openai/gpt-5.6-luna", "high": "anthropic/claude-sonnet-5"} {
		if err := slots.Set(t.Context(), "default", profile, pattern, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	if err := home.StageAgentDir(t.Context(), dir, idstest.N(1)); err != nil {
		t.Fatal(err)
	}
	worker, _ := os.ReadFile(filepath.Join(dir, "agents", "worker.md"))
	if !strings.Contains(string(worker), "model: anthropic/claude-sonnet-5") {
		t.Fatalf("project profile must reach the worker:\n%s", worker)
	}
	// fast is unmapped here: scout degrades to the project default rather
	// than to a dead slot.
	scout, _ := os.ReadFile(filepath.Join(dir, "agents", "scout.md"))
	if !strings.Contains(string(scout), "model: anthropic/claude-sonnet-5") {
		t.Fatalf("unmapped fast slot must fall back to default:\n%s", scout)
	}
}
