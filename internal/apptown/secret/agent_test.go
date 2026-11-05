package secret

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// Delivery is opt-in, preview-only, per launch, and requires evidence of the
// actual version. Missing unrelated secrets must never block this delivery.
func TestAgentPreviewDeliveryRequiresOptInAndEvidence(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ctx := t.Context()
	id, err := database.Transact(ctx, db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(ctx, tx, time.Now(), project.Spec{Slug: "demo", Name: "Demo"})
	})
	if err != nil {
		t.Fatal(err)
	}
	accounts := identity.New(db, identity.Config{OpenInstance: true})
	if err := accounts.SeedAdministrator(ctx, "boss"); err != nil {
		t.Fatal(err)
	}
	ctx = access.WithCaller(ctx, access.Caller{Principal: access.Principal{Kind: "user", Name: "boss", Via: "session"}, Verified: true})
	s := NewService(db, Config{Projects: testSecretProjectScope{db}, Authority: access.Checked{Authority: accounts, Projects: project.New(db, accounts)}})
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, SpecFile), []byte("[project]\nname = \"demo\"\nrevision = \"1.0\"\n[profiles.preview]\nNEXTUP_PASSWORD = { required = true }\nUNRELATED = { required = true }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	const value = "preview-canary-keep-out"
	for _, dep := range []string{"main", "preview"} {
		if _, err := s.BindStored(ctx, id, "demo", dep, "NEXTUP_PASSWORD", map[string]string{"main": "main-canary-keep-out", "preview": value}[dep], "boss"); err != nil {
			t.Fatal(err)
		}
	}
	launch := AgentLaunch{Kind: "agent", ChangeID: idstest.N(8), RunID: idstest.N(9), StepID: idstest.N(10), Attempt: 2, Invocation: "agent"}
	env, err := s.AgentEnv(ctx, id, ws, launch)
	if err != nil || len(env) != 0 {
		t.Fatal("default delivery is not empty", err)
	}
	if err := s.SetAgentVisible(ctx, id, "main", "NEXTUP_PASSWORD", true); err == nil {
		t.Fatal("main toggle accepted")
	}
	restore := dbtest.NextStatementFailure(db, "-- name: AppendReceipt")
	err = s.SetAgentVisible(ctx, id, "preview", "NEXTUP_PASSWORD", true)
	restore()
	if err == nil {
		t.Fatal("toggle accepted without receipt")
	}
	b, err := s.Bindings.For(ctx, id, "preview")
	if err != nil || b["NEXTUP_PASSWORD"].AgentVisible {
		t.Fatal("failed toggle persisted", err)
	}
	if err := s.SetAgentVisible(ctx, id, "preview", "NEXTUP_PASSWORD", true); err != nil {
		t.Fatal(err)
	}
	// Resolution must report the latest stored version, not the old binding version.
	if _, err := s.Vault.Put(ctx, Scope("demo", "preview", ""), "NEXTUP_PASSWORD", value, "boss"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"agent/review", "project-chat"} {
		excluded := launch
		excluded.Kind = kind
		env, err := s.AgentEnv(ctx, id, ws, excluded)
		if err != nil || len(env) != 0 {
			t.Fatal("excluded session received values", err)
		}
	}
	excluded := launch
	excluded.ChangeID = ids.Nil
	env, err = s.AgentEnv(ctx, id, ws, excluded)
	if err != nil || len(env) != 0 {
		t.Fatal("project session received values", err)
	}
	for _, kind := range []string{"agent", "change-chat", "change-chat"} {
		launch.Kind = kind
		launch.Session = "chat-one"
		env, err := s.AgentEnv(ctx, id, ws, launch)
		if err != nil || len(env) != 1 || env[0] != "NEXTUP_PASSWORD="+value {
			t.Fatal("preview delivery failed", err)
		}
	}
	records, err := audit.Recent(ctx, db, audit.Filter{ProjectID: id, KindPrefix: "secret.agent-delivery"})
	if err != nil || len(records) != 3 {
		t.Fatal("missing per-launch evidence", err)
	}
	subjects := map[string]bool{}
	for _, r := range records {
		payload := string(r.Payload)
		for _, want := range []string{"NEXTUP_PASSWORD", "preview", "internal", `"version":"2"`, `"attempt":2`} {
			if !strings.Contains(payload, want) {
				t.Errorf("evidence missing %s", want)
			}
		}
		if strings.Contains(payload, value) || strings.Contains(payload, "main-canary") {
			t.Fatal("evidence contains plaintext")
		}
		subjects[r.Subject] = true
	}
	if len(subjects) != 3 {
		t.Fatal("wakes reused launch identity")
	}
	restore = dbtest.NextStatementFailure(db, "-- name: AppendReceipt")
	env, err = s.AgentEnv(ctx, id, ws, launch)
	restore()
	if err == nil || len(env) != 0 {
		t.Fatal("delivery without evidence")
	}
	if err := s.SetAgentVisible(ctx, id, "preview", "NEXTUP_PASSWORD", false); err != nil {
		t.Fatal(err)
	}
	env, err = s.AgentEnv(ctx, id, ws, launch)
	if err != nil || len(env) != 0 {
		t.Fatal("disabled delivery", err)
	}
	if err := s.SetAgentVisible(ctx, id, "preview", "NEXTUP_PASSWORD", true); err != nil {
		t.Fatal(err)
	}
	// Repointing a visible binding at project storage stops delivery on the next launch.
	version, err := s.Vault.Put(ctx, "project:demo", "NEXTUP_PASSWORD", "shared-canary", "boss")
	if err != nil || version != 1 {
		t.Fatal(err)
	}
	if err := s.Bindings.Put(ctx, id, Binding{Deployment: "preview", Name: "NEXTUP_PASSWORD", Store: StoreInternal, Ref: "project:demo", Version: "1", UpdatedBy: "boss"}); err != nil {
		t.Fatal(err)
	}
	env, err = s.AgentEnv(ctx, id, ws, launch)
	if err != nil || len(env) != 0 {
		t.Fatal("repointed shared value delivered", err)
	}
	if err := s.Bindings.Unbind(ctx, id, "preview", "NEXTUP_PASSWORD", "boss"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindStored(ctx, id, "demo", "preview", "NEXTUP_PASSWORD", value, "boss"); err != nil {
		t.Fatal(err)
	}
	b, err = s.Bindings.For(ctx, id, "preview")
	if err != nil || b["NEXTUP_PASSWORD"].AgentVisible {
		t.Fatal("unbind retained disclosure", err)
	}
}

func TestAgentVisibilityChecksCurrentSecretAuthority(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ctx := t.Context()
	id, err := database.Transact(ctx, db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(ctx, tx, time.Now(), project.Spec{Slug: "demo", Name: "Demo"})
	})
	if err != nil {
		t.Fatal(err)
	}
	accounts := identity.New(db, identity.Config{OpenInstance: true})
	if _, err := accounts.EnsureUser(ctx, "member"); err != nil {
		t.Fatal(err)
	}
	s := NewService(db, Config{Projects: testSecretProjectScope{db}, Authority: access.Checked{Authority: accounts, Projects: project.New(db, accounts)}})
	if _, err := s.BindStored(ctx, id, "demo", "preview", "PASSWORD", "short", "fixture"); err != nil {
		t.Fatal(err)
	}
	for _, principal := range []access.Principal{{Kind: "user", Name: "member", Via: "session"}, {Kind: "instrument", Name: "run/1", Instrument: "run", ProjectID: id}, {Kind: "instrument", Name: "member via console/c", Person: "member", Instrument: "console", ProjectID: id}} {
		caller := access.WithCaller(ctx, access.Caller{Principal: principal, Verified: true})
		if err := s.SetAgentVisible(caller, id, "preview", "PASSWORD", true); err == nil {
			t.Fatal("caller without secret:bind toggled visibility")
		}
		if err := s.SeparatePreview(caller, id, "PASSWORD"); err == nil {
			t.Fatal("caller without secret:bind separated value")
		}
	}
}

type testSecretProjectScope struct{ db *database.DB }

func (s testSecretProjectScope) Slug(ctx context.Context, id ids.ID) (string, error) {
	p, err := project.ByIDIn(ctx, s.db, id)
	return p.Slug, err
}
