package secret

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids"
)

// One deployment, one profile, for both files: a choice app.yaml alone
// declares holds for secrets too, which then use their default rather
// than choosing their own preview; and choosing default returns both
// files to their base, even on a preview.
func TestSecretsFollowTheDeploymentsOneProfile(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "demo", Name: "Demo"})
	})
	if err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	spec := "[project]\nname = \"demo\"\nrevision = \"1.0\"\n\n" +
		"[profiles.default]\nMODE = { description = \"mode\", default = \"base\" }\n\n" +
		"[profiles.preview]\nMODE = { description = \"mode\", default = \"preview\" }\n"
	if err := os.WriteFile(filepath.Join(ws, SpecFile), []byte(spec), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := appcontract.Parse([]byte("app:\n  web: {image: app:1, public: true}\nprofiles:\n  large:\n    web: {resources: {memory: 2G}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(db, Config{DataDir: t.TempDir()})
	mode := func(deployment string) (string, string) {
		d, err := s.Resolve(t.Context(), id, "demo", deployment, ws, c)
		if err != nil {
			t.Fatal(err)
		}
		return d.Profile, d.EnvFor("web")["MODE"]
	}
	if p, v := mode("preview"); p != "preview" || v != "preview" {
		t.Fatalf("a preview with no choice: profile %q, MODE %q; want SecretSpec's preview", p, v)
	}
	if err := s.Profiles.Set(t.Context(), id, "preview", "large"); err != nil {
		t.Fatal(err)
	}
	name, how, err := s.DeploymentProfile(t.Context(), id, "preview", nil, c)
	if err != nil || name != "large" || how != appcontract.ProfileChosen {
		t.Fatalf("the preview's profile: %q %q %v", name, how, err)
	}
	if p, v := mode("preview"); p != "default" || v != "base" {
		t.Errorf("a preview chosen as large, which SecretSpec does not declare: profile %q, MODE %q; want its default", p, v)
	}
	if err := s.Profiles.Set(t.Context(), id, "preview", "default"); err != nil {
		t.Fatal(err)
	}
	if name, how, _ := s.DeploymentProfile(t.Context(), id, "preview", nil, c); name != "default" || how != appcontract.ProfileChosen {
		t.Errorf("choosing default on a preview: %q %q; want default, chosen", name, how)
	}
	if p, v := mode("preview"); p != "default" || v != "base" {
		t.Errorf("a preview returned to default: profile %q, MODE %q", p, v)
	}
}

// A SecretSpec need not declare default; such a file uses its first
// profile in name order when the deployment's profile is not among its
// own, as it always has.
func TestASecretSpecWithoutADefaultUsesItsFirstProfile(t *testing.T) {
	t.Parallel()
	spec := &Spec{Profiles: map[string]map[string]DeclaredSecret{"staging": {}, "production": {}}}
	if got := SpecProfile(spec, "production"); got != "production" {
		t.Errorf("a declared profile: %q", got)
	}
	if got := SpecProfile(spec, "large"); got != "production" {
		t.Errorf("an undeclared profile in a file without default: %q, want its first, production", got)
	}
	spec.Profiles["default"] = map[string]DeclaredSecret{}
	if got := SpecProfile(spec, "large"); got != "default" {
		t.Errorf("an undeclared profile in a file with default: %q", got)
	}
}
