package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/integration/internal/integrationsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

func requiredAttachmentFixture(t *testing.T) (*database.DB, ids.ID) {
	t.Helper()
	db := testDB(t)
	first := createTestProject(t, db, "first", "First")
	second := createTestProject(t, db, "second", "Second")
	bindings := NewWithClock(db, nil)
	for _, binding := range [][3]string{{"git", "scm", "gitlab"}, {"errors", "errors", "glitchtip"}} {
		if _, err := bindings.Save(context.Background(), binding[0], binding[1], binding[2], `{}`, "tester"); err != nil {
			t.Fatal(err)
		}
	}
	attachments := NewAttachments(db)
	if err := attachments.Attach(t.Context(), first, "scm", "git", `{}`, "tester", nil); err != nil {
		t.Fatal(err)
	}
	if err := attachments.Attach(t.Context(), second, "errors", "errors", `{}`, "tester", nil); err != nil {
		t.Fatal(err)
	}
	return db, first
}

func TestRequiredCapabilitiesAreScopedRealAttachments(t *testing.T) {
	t.Parallel()
	for _, example := range []struct {
		name     string
		required []string
		want     bool
	}{
		{name: "attached", required: []string{"scm", "scm"}, want: true},
		{name: "other project", required: []string{"errors"}},
		{name: "no requirements", want: true},
		{name: "unnamed", required: []string{" "}},
		{name: "private cache", required: []string{"scm"}},
		{name: "missing binding", required: []string{"scm"}},
		{name: "wrong binding capability", required: []string{"scm"}},
	} {
		t.Run(example.name, func(t *testing.T) {
			t.Parallel()
			db, first := requiredAttachmentFixture(t)
			switch example.name {
			case "private cache":
				if _, err := integrationsql.New(db.SQL()).AttachIntegration(context.Background(), integrationsql.AttachIntegrationParams{ProjectID: first, Capability: "scm", Binding: "git", ResourceJson: []byte(`{}`), Provenance: "", CreatedAt: time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
			case "missing binding":
				if n, err := integrationsql.New(db.SQL()).DeleteBinding(context.Background(), "git"); err != nil || n != 1 {
					t.Fatalf("missing binding: %d %v", n, err)
				}
			case "wrong binding capability":
				// sql-guarantee: Make an existing attachment disagree with its binding capability; binding capability is immutable in both owner commands and generated updates.
				if _, err := db.SQL().ExecContext(context.Background(), `UPDATE integrations SET capability='errors' WHERE name='git'`); err != nil {
					t.Fatal(err)
				}
			}
			err := HasCapabilitiesIn(context.Background(), db, first, example.required)
			if (err == nil) != example.want {
				t.Fatalf("required %v: %v", example.required, err)
			}
		})
	}
	db, first := requiredAttachmentFixture(t)
	drop := dbtest.NextStatementFailure(db, "AttachedCapabilitiesForShare")
	if err := HasCapabilitiesIn(context.Background(), db, first, []string{"scm"}); err == nil {
		t.Fatal("failed read accepted")
	}
	drop()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := HasCapabilitiesIn(ctx, db, first, []string{"scm"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestRequiredCapabilitiesHoldAttachmentAndBindingUntilSettlement(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name, waiting string
		mutate        func(context.Context, *database.DB, ids.ID) error
	}{
		{name: "detach", waiting: "-- name: DetachIntegration", mutate: func(ctx context.Context, db *database.DB, first ids.ID) error {
			n, err := integrationsql.New(db.SQL()).DetachIntegration(ctx, integrationsql.DetachIntegrationParams{ProjectID: first, Capability: "scm"})
			if err != nil {
				return err
			}
			if n != 1 {
				return fmt.Errorf("detached %d attachments", n)
			}
			return nil
		}},
		{name: "change capability", waiting: "UPDATE integrations SET capability", mutate: func(ctx context.Context, db *database.DB, _ ids.ID) error {
			// sql-guarantee: Attempt a capability reassignment while HasCapabilitiesIn holds its share lock; no binding update changes capability, and replacing the row tests a different lock.
			_, err := db.SQL().ExecContext(ctx, `UPDATE integrations SET capability='changed' WHERE name='git'`)
			return err
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			db, first := requiredAttachmentFixture(t)
			tx, err := db.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := HasCapabilitiesIn(context.Background(), tx, first, []string{"scm"}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- scenario.mutate(ctx, db, first) }()
			dbtest.WaitForLock(t, db, scenario.waiting)
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if err := HasCapabilitiesIn(context.Background(), db, first, []string{"scm"}); err == nil {
				t.Fatal("changed dependency accepted")
			}
		})
	}
}
