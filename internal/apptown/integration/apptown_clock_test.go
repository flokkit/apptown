package integration

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/clock/clocktest"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// What the integrations owner stores is stamped by AppTown's clock, not
// the wall clock or the database's: on a clock ninety days behind, a
// binding's version, a service kind and an attachment carry that clock's
// time.
func TestAppTownsClockStampsBindingsKindsAndAttachments(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	at := time.Now().Add(-90 * 24 * time.Hour).UTC().Truncate(time.Microsecond)
	s := NewService(db, Dependencies{Projects: project.New(db, nil), Secrets: secret.NewService(db, secret.Config{DataDir: t.TempDir()}), Now: func() time.Time { return at }})
	if _, err := s.Bindings.Upsert(t.Context(), "semgrep", "scanner", "tool", `{"image":"semgrep:1"}`, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Bindings.StockUpdate(t.Context(), "semgrep", `{"image":"semgrep:2"}`); err != nil {
		t.Fatal(err)
	}
	if b, err := s.Bindings.ByName(t.Context(), "semgrep"); err != nil || b == nil || b.UpdatedAt != at.Format(time.RFC3339Nano) {
		t.Fatalf("a binding's version is stamped %+v, not %s: %v", b, at, err)
	}
	if err := s.ServiceKinds.Save(context.Background(), "mailer", "capability: mailer\ntitle: Mailer\nenv:\n  - {key: URL, value: 'https://mailer.example'}\n", "Mailer", "ops"); err != nil {
		t.Fatal(err)
	}
	kinds, err := s.ServiceKinds.All(t.Context())
	if err != nil || len(kinds) != 1 || kinds[0].UpdatedAt != at.Format(time.RFC3339Nano) {
		t.Fatalf("a service kind is stamped %+v, not %s: %v", kinds, at, err)
	}
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Attachments.Attach(t.Context(), id, "mailer", "", `{}`, "ops", nil); err != nil {
		t.Fatal(err)
	}
	rows, err := AttachedRows(context.Background(), db, id)
	if err != nil || len(rows) == 0 {
		t.Fatalf("attachments: %+v %v", rows, err)
	}
	for _, r := range rows {
		if !r.CreatedAt.Equal(at) {
			t.Fatalf("an attachment is stamped %s, not %s", r.CreatedAt, at)
		}
	}
}

// A binding's save and an attachment take one reading of AppTown's clock
// for the record and its receipt. On a clock that moves on a second at
// every reading, a receipt stamped by a second reading would say a later
// second than the record.
func TestAnIntegrationReceiptCarriesItsRecordsTime(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	clk := clocktest.NewStepping(time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC), time.Second)
	s := NewService(db, Dependencies{Projects: project.New(db, nil), Secrets: secret.NewService(db, secret.Config{DataDir: t.TempDir()}), Now: clk.Now})
	receiptAt := func(kind string) time.Time {
		t.Helper()
		got, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: kind})
		if err != nil || len(got) == 0 {
			t.Fatalf("%s receipts: %+v %v", kind, got, err)
		}
		at, err := time.Parse(time.RFC3339Nano, got[0].At)
		if err != nil {
			t.Fatal(err)
		}
		return at
	}
	if _, err := s.Bindings.Save(context.Background(), "mail", "mailer", "smtp", `{}`, "ops"); err != nil {
		t.Fatal(err)
	}
	b, err := s.Bindings.ByName(t.Context(), "mail")
	if err != nil || b == nil {
		t.Fatalf("binding: %+v %v", b, err)
	}
	if record, receipt := b.UpdatedAt, receiptAt("integration.saved"); record != receipt.Format(time.RFC3339Nano) {
		t.Errorf("a binding saved at %s, its receipt at %v", record, receipt)
	}

	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Attachments.Attach(t.Context(), id, "mailer", "mail", `{}`, "ops", nil); err != nil {
		t.Fatal(err)
	}
	rows, err := AttachedRows(context.Background(), db, id)
	if err != nil || len(rows) != 1 {
		t.Fatalf("attachments: %+v %v", rows, err)
	}
	if record, receipt := rows[0].CreatedAt, receiptAt("integration.attached"); !record.Equal(receipt) {
		t.Errorf("an attachment made at %v, its receipt at %v", record, receipt)
	}
}

// A binding committed for a caller that then went away keeps its receipt:
// the receipt's whole transaction outlives the caller.
//
// Kills: the receipt transaction begun on the caller's context.
func TestABindingSavedForAGoneCallerKeepsItsReceipt(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := NewService(db, Dependencies{Projects: project.New(db, nil), Secrets: secret.NewService(db, secret.Config{DataDir: t.TempDir()})})
	ctx, cancel := context.WithCancel(t.Context())
	s.Bindings.committed = cancel // the caller goes away once the binding has committed
	_, _ = s.Bindings.Save(ctx, "mail", "mailer", "smtp", `{}`, "ops")
	b, err := s.Bindings.ByName(t.Context(), "mail")
	if err != nil || b == nil {
		t.Fatalf("the binding was not committed: %+v %v", b, err)
	}
	got, err := audit.Recent(t.Context(), db, audit.Filter{KindPrefix: "integration.saved"})
	if err != nil || len(got) != 1 || got[0].Actor != "ops" {
		t.Fatalf("the committed binding has no receipt: %+v %v", got, err)
	}
}
