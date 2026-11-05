package secret

import (
	"errors"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids"
)

// Project-generated values stay shared for app delivery. Agent sharing requires
// a separately stored preview value and never rotates main.
func TestAgentSharingSeparatesProjectGeneratedPreviewWithoutChangingMain(t *testing.T) {
	db := testDB(t)
	id, err := database.Transact(t.Context(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "review-scope", Name: "Review scope"})
	})
	if err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	declaration := "[project]\nname = \"review-scope\"\nrevision = \"1.0\"\n[profiles.default]\nSIGNING_KEY = { generate = true }\n"
	if err := os.WriteFile(filepath.Join(ws, SpecFile), []byte(declaration), 0600); err != nil {
		t.Fatal(err)
	}
	c := &appcontract.Contract{Parts: []appcontract.Part{{Name: "web"}}, SecretGenerate: map[string]string{"SIGNING_KEY": "project"}}
	accounts := identity.New(db, identity.Config{OpenInstance: true})
	if err := accounts.SeedAdministrator(t.Context(), "boss"); err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{Kind: "user", Name: "boss", Via: "session"}, Verified: true})
	s := NewService(db, Config{Projects: testSecretProjectScope{db}, DataDir: t.TempDir(), Authority: access.Checked{Authority: accounts, Projects: project.New(db, accounts)}})
	main, err := s.Resolve(t.Context(), id, "review-scope", "main", ws, c)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.Resolve(t.Context(), id, "review-scope", "preview", ws, c)
	if err != nil {
		t.Fatal(err)
	}
	if main.EnvFor("web")["SIGNING_KEY"] == "" {
		t.Fatal("deployment received no value")
	}
	if main.EnvFor("web")["SIGNING_KEY"] != preview.EnvFor("web")["SIGNING_KEY"] {
		t.Fatal("project generation no longer shares app values")
	}
	for _, deployment := range []string{"main", "preview"} {
		bindings, err := s.Bindings.For(t.Context(), id, deployment)
		if err != nil {
			t.Fatal(err)
		}
		if bindings["SIGNING_KEY"].Ref != "project:review-scope" {
			t.Fatal("unexpected binding scope")
		}
	}
	before, err := s.Bindings.For(ctx, id, "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentVisible(ctx, id, "preview", "SIGNING_KEY", true); err == nil || !strings.Contains(err.Error(), sharedPreviewValue) {
		t.Fatal("shared preview toggle was not refused")
	}
	restore := dbtest.NextStatementFailure(db, "-- name: AppendReceipt")
	err = s.SeparatePreview(ctx, id, "SIGNING_KEY")
	restore()
	if err == nil {
		t.Fatal("separation succeeded without its receipt")
	}
	if _, _, err := s.Vault.Get(ctx, Scope("review-scope", "preview", ""), "SIGNING_KEY"); !errors.Is(err, ErrNotStored) {
		t.Fatal("failed separation persisted a preview value")
	}
	shared, err := s.Bindings.For(ctx, id, "preview")
	if err != nil || shared["SIGNING_KEY"].Ref != before["SIGNING_KEY"].Ref {
		t.Fatal("failed separation changed binding", err)
	}
	if err := s.SeparatePreview(ctx, id, "SIGNING_KEY"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentVisible(ctx, id, "preview", "SIGNING_KEY", true); err != nil {
		t.Fatal(err)
	}
	launch := AgentLaunch{Kind: "agent", ChangeID: idstest.N(1), StepID: idstest.N(2), Attempt: 1}
	env, err := s.AgentEnv(ctx, id, ws, launch)
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.Bindings.For(ctx, id, "main")
	if err != nil {
		t.Fatal(err)
	}
	if before["SIGNING_KEY"] != after["SIGNING_KEY"] {
		t.Fatal("main binding changed")
	}
	mainAfter, version, err := s.Vault.Get(ctx, before["SIGNING_KEY"].Ref, "SIGNING_KEY")
	if err != nil || mainAfter != main.EnvFor("web")["SIGNING_KEY"] || version != 1 {
		t.Fatal("main value or version changed")
	}
	previewAfter, err := s.Resolve(ctx, id, "review-scope", "preview", ws, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 1 || env[0] != "SIGNING_KEY="+previewAfter.EnvFor("web")["SIGNING_KEY"] || previewAfter.EnvFor("web")["SIGNING_KEY"] == mainAfter {
		t.Fatal("agent did not receive only independent preview value")
	}

}
