package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// An eval copy is given no integration: the offered registry is not
// attached, nothing is attached or remembered for it, and nothing is
// provisioned. Its releases go to the one offered registry connector under
// the copy's own repository, with the connector's credential, read by the
// AppTown and never attached to the copy.
func TestACopyIsGivenNoIntegrationAndPublishesUnderItsOwnRepository(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	dir := t.TempDir()
	copy, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "shop-copy", Name: "Shop copy", Purpose: project.EvalCopy})
	})
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(db, Dependencies{Projects: project.New(db, nil), Secrets: secret.NewService(db, secret.Config{DataDir: dir})})
	if _, err := s.Bindings.Upsert(t.Context(), "registry", CapContainerRegistry, "service", `{"url":"registry.example","group":"team","username":"robot","tokenFrom":["REGISTRY_PASSWORD"]}`, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("REGISTRY_PASSWORD=forge-held\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachOfferedRegistry(context.Background(), copy, "apptown-system", nil); !errors.Is(err, ErrNoIntegrations) {
		t.Fatalf("the offered registry attached to a copy: %v", err)
	}
	if err := s.Attachments.Attach(t.Context(), copy, CapErrors, "errors", `{}`, Entered, "someone", nil); !errors.Is(err, ErrNoIntegrations) {
		t.Fatalf("an attachment for a copy: %v", err)
	}
	if err := s.Attachments.Remember(t.Context(), copy, "scm", "gitlab", `{}`); !errors.Is(err, ErrNoIntegrations) {
		t.Fatalf("a remembered resource for a copy: %v", err)
	}
	p, _ := s.projects.ByID(context.Background(), copy)
	if _, _, err := s.ProvisionRegistry(t.Context(), &p, "registry", "apptown-system"); !errors.Is(err, ErrNoIntegrations) {
		t.Fatalf("a registry provisioned for a copy: %v", err)
	}
	if attached, err := s.Attachments.IsAttached(t.Context(), copy, CapContainerRegistry); err != nil || attached {
		t.Fatalf("the copy holds an attachment: %v %v", attached, err)
	}
	// Nothing the copy declares is pulled with AppTown's credential.
	if got, err := s.Registry(context.Background(), copy); err != nil || got != nil || s.RegistryFor(context.Background(), copy) != nil {
		t.Fatalf("a copy's general registry: %+v %v", got, err)
	}
	got, err := s.EvalPublication(t.Context(), copy)
	if err != nil || got == nil || got.Repo != "registry.example/team/shop-copy" || got.Username != "robot" || got.Token != "forge-held" {
		t.Fatalf("the copy's own repository: %+v %v", got, err)
	}
	// A project Project cannot read is given nothing either.
	if err := s.Attachments.Attach(t.Context(), idstest.N(999999), CapErrors, "errors", `{}`, Entered, "someone", nil); err == nil {
		t.Fatal("an attachment for a project Project cannot read")
	}
	// Publication through AppTown's registry is a copy's act alone.
	ordinary, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "shop", Name: "Shop"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.EvalPublication(t.Context(), ordinary); err == nil {
		t.Fatalf("an ordinary project publishes through AppTown's credential: %+v", got)
	}
	if _, err := s.Bindings.Upsert(t.Context(), "gitlab-registry", CapContainerRegistry, "gitlab", `{"provider":"gitlab","url":"registry.gitlab.example"}`, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EvalPublication(t.Context(), copy); err == nil {
		t.Fatal("with two registry connectors AppTown cannot choose one for a copy")
	}
}
