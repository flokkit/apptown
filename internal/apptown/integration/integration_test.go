package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/integration/internal/integrationsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestBindingsVersionsAndRules(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	bs := NewWithClock(db, nil)
	if v, err := bs.Upsert(t.Context(), "semgrep", "scanner", "tool", `{"image":"semgrep:1"}`, true); err != nil || v != 1 {
		t.Fatalf("first: %d %v", v, err)
	}
	if v, err := bs.Upsert(t.Context(), "semgrep", "scanner", "tool", `{"image": "semgrep:1"}`, true); err != nil || v != 1 {
		t.Fatalf("same configuration, spaced differently, is no new version: %d %v", v, err)
	}
	if v, err := bs.Upsert(t.Context(), "semgrep", "scanner", "tool", `{"image":"semgrep:2"}`, true); err != nil || v != 2 {
		t.Fatalf("changed: %d %v", v, err)
	}
	// sql-guarantee: The append-only version history is the guarantee, and no owner read lists integration_versions rows.
	dbtest.Count(t, db, 2, `SELECT COUNT(*) FROM integration_versions`)
	if _, err := bs.Save(context.Background(), "semgrep", "scanner", "tool", `{}`, "alice"); !errors.Is(err, ErrManaged) {
		t.Fatalf("a person overwrote a managed binding: %v", err)
	}
	if err := bs.Delete(t.Context(), "semgrep", "alice"); !errors.Is(err, ErrManaged) {
		t.Fatalf("a managed binding was deleted: %v", err)
	}
	if _, err := bs.Save(context.Background(), "mine", "scanner", "tool", `{"image":"x"}`, "alice"); err != nil {
		t.Fatal(err)
	}
	if saved := integrationReceipts(t, db, "integration.saved", "mine"); len(saved) != 1 || saved[0].Actor != "alice" {
		t.Fatalf("save receipt: %+v", saved)
	}
	projectID := createTestProject(t, db, "p", "P")
	attachments := NewAttachments(db)
	if err := attachments.Attach(t.Context(), projectID, "scanner", "mine", `{}`, Entered, "alice", nil); err != nil {
		t.Fatal(err)
	}
	if err := bs.Delete(t.Context(), "mine", "alice"); !errors.Is(err, ErrAttached) {
		t.Fatalf("a load-bearing binding was deleted: %v", err)
	}
	if err := attachments.Detach(context.Background(), projectID, "scanner", "test"); err != nil {
		t.Fatal(err)
	}
	if err := bs.Delete(t.Context(), "mine", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := bs.Delete(t.Context(), "mine", "alice"); !errors.Is(err, ErrNoBinding) || fault.KindOf(err) != fault.NotFound || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("absence: %v", err)
	}
	snap, err := Selected(context.Background(), db, "scanner", "")
	if err != nil || snap.Name != "semgrep" || snap.Version != 2 {
		t.Fatalf("the earliest of the capability: %+v %v", snap, err)
	}

	kinds := NewServiceKinds(db)
	if err := kinds.Save(context.Background(), "cache", "capability: cache\ntitle: Cache\n", "Cache", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := attachments.Attach(t.Context(), projectID, "cache", "redis", `{}`, Entered, "alice", nil); err != nil {
		t.Fatal(err)
	}
	if err := kinds.Delete(t.Context(), "cache", "alice"); !errors.Is(err, ErrInUse) {
		t.Fatalf("a kind in use was deleted: %v", err)
	}
	if err := kinds.Delete(t.Context(), "nothing", "alice"); !errors.Is(err, ErrNoServiceKind) || fault.KindOf(err) != fault.NotFound || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("absence: %v", err)
	}
}

func TestAttachmentsRulesAndReceipts(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	at := NewAttachments(db)
	p := createTestProject(t, db, "att", "Att")
	if err := at.Attach(t.Context(), p, "errors", "glitchtip", `{"dsn":"x"}`, "", "alice", nil); err == nil {
		t.Fatal("an attachment without a provenance was recorded")
	}
	if err := at.Attach(t.Context(), p, "errors", "glitchtip", `{"dsn":"x"}`, Provisioned, "alice", map[string]any{"providerProject": "att"}); err != nil {
		t.Fatal(err)
	}
	attached := integrationReceipts(t, db, "integration.attached", "errors")
	if len(attached) != 1 || attached[0].ProjectID != p {
		t.Fatalf("attach receipt: %+v", attached)
	}
	// A map, not a struct: encoding/json matches struct fields without regard
	// to case, and the receipt's keys are exactly providerProject and provenance.
	var payload map[string]any
	if err := json.Unmarshal(attached[0].Payload, &payload); err != nil || payload["providerProject"] != "att" || payload["provenance"] != "provisioned" {
		t.Fatalf("attach receipt payload: %s %v", attached[0].Payload, err)
	}
	if ok, _ := at.IsAttached(t.Context(), p, "errors"); !ok {
		t.Fatal("attached")
	}
	if b, res, err := at.Attached(context.Background(), p, "errors"); err != nil || b != "glitchtip" || res != `{"dsn": "x"}` && res != `{"dsn":"x"}` {
		t.Fatalf("attached: %s %s %v", b, res, err)
	}
	if err := at.Remember(t.Context(), p, "scm", "gitlab", `{"profiles":{}}`); err != nil {
		t.Fatal(err)
	}
	if ok, _ := at.IsAttached(t.Context(), p, "scm"); ok {
		t.Fatal("an engine-internal row is not an attachment")
	}
	if _, _, err := at.Attached(context.Background(), p, "scm"); !errors.Is(err, ErrNotAttached) {
		t.Fatalf("engine-internal rows read as not attached: %v", err)
	}
	link := func(capability string, change ids.ID) error {
		return db.InTx(context.Background(), func(tx *database.Tx) error {
			return LinkWireIn(context.Background(), tx, p, capability, change)
		})
	}
	// Change 42 does not exist, so it is no live work and a later link replaces it.
	for _, change := range []ids.ID{idstest.N(42), idstest.N(43)} {
		if err := link("errors", change); err != nil {
			t.Fatal(err)
		}
	}
	// The package's own lock read shows the stored link, even to a Change that
	// does not exist.
	linked := func() ids.ID {
		t.Helper()
		row, err := database.Transact(context.Background(), db, func(tx *database.Tx) (integrationsql.AttachmentWireForUpdateRow, error) {
			return integrationsql.New(tx.SQL()).AttachmentWireForUpdate(context.Background(), integrationsql.AttachmentWireForUpdateParams{ProjectID: p, Capability: "errors"})
		})
		if err != nil {
			t.Fatal(err)
		}
		return row.WireChangeID
	}
	if got := linked(); got != idstest.N(43) {
		t.Fatalf("a link to no live work is replaced: %d", got)
	}
	for _, capability := range []string{"scm", "absent"} {
		if err := link(capability, idstest.N(44)); fault.KindOf(err) != fault.NotFound {
			t.Fatalf("linking %s, which is not attached: %v", capability, err)
		}
	}
	if got := linked(); got != idstest.N(43) {
		t.Fatalf("a refused link wrote: %d", got)
	}
	if err := at.Detach(context.Background(), p, "errors", "test"); err != nil {
		t.Fatal(err)
	}
	if err := at.Detach(context.Background(), p, "errors", "test"); !errors.Is(err, ErrNotAttached) {
		t.Fatalf("detaching twice: %v", err)
	}
	if err := at.Detach(context.Background(), p, "scm", "test"); !errors.Is(err, ErrNotAttached) {
		t.Fatalf("an engine-internal row is never detached: %v", err)
	}
}

// Generated statements must keep the pool's fault hook, so a failed version
// record cannot leave the binding pointing at a version that does not exist.
func TestBindingVersionFailureRollsBackConfiguration(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	bindings := NewWithClock(db, nil)
	if _, err := bindings.Upsert(t.Context(), "cache", "cache", "redis", `{"image":"one"}`, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.FailNextStatement("INSERT INTO integration_versions"))
	if _, err := bindings.Upsert(t.Context(), "cache", "cache", "redis", `{"image":"two"}`, false); err == nil {
		t.Fatal("version failure was swallowed")
	}
	row, err := bindings.ByName(t.Context(), "cache")
	if err != nil || row.Version != 1 || !sameJSON(row.ConfigJSON, `{"image":"one"}`) {
		t.Fatalf("configuration escaped rollback: %+v %v", row, err)
	}
	// sql-guarantee: The append-only version history is the guarantee, and no owner read lists integration_versions rows.
	dbtest.Count(t, db, 1, `SELECT COUNT(*) FROM integration_versions`)
}

func TestCreateBindingParticipatesInCallerTransaction(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"success", "rollback", "version-failure", "receipt-failure", "existing", "managed"} {
		t.Run(scenario, func(t *testing.T) {
			db := testDB(t)
			bindings := NewWithClock(db, nil)
			called := false
			bindings.committed = func() { called = true }
			var before *Binding
			wantVersions := 0
			if scenario == "existing" || scenario == "managed" {
				if _, err := bindings.Upsert(t.Context(), "account", "cloud-account", "digitalocean", `{"region":"original"}`, scenario == "managed"); err != nil {
					t.Fatal(err)
				}
				var err error
				before, err = bindings.ByName(t.Context(), "account")
				if err != nil {
					t.Fatal(err)
				}
				wantVersions = 1
			}
			if scenario == "version-failure" {
				t.Cleanup(db.FailNextStatement("-- name: InsertBindingVersion"))
			}
			if scenario == "receipt-failure" {
				t.Cleanup(db.FailNextStatement("-- name: AppendReceipt"))
			}
			rollback := errors.New("caller refused")
			err := db.InTx(t.Context(), func(tx *database.Tx) error {
				if err := bindings.CreateIn(t.Context(), tx, "account", "cloud-account", "digitalocean", `{"region":"replacement"}`, "fixture"); err != nil {
					return err
				}
				if scenario == "rollback" {
					return rollback
				}
				return nil
			})
			if scenario == "success" {
				if err != nil {
					t.Fatal(err)
				}
				wantVersions = 1
			} else if err == nil {
				t.Fatal("failed admission succeeded")
			}
			if before != nil && fault.KindOf(err) != fault.Conflict {
				t.Fatalf("duplicate: %v", err)
			}
			after, readErr := bindings.ByName(t.Context(), "account")
			switch {
			case before != nil:
				if readErr != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("existing binding changed: before=%+v after=%+v err=%v", before, after, readErr)
				}
			case scenario == "success":
				if readErr != nil || after.Version != 1 || after.Managed || !sameJSON(after.ConfigJSON, `{"region":"replacement"}`) {
					t.Fatalf("created binding: %+v %v", after, readErr)
				}
			default:
				if !errors.Is(readErr, sql.ErrNoRows) {
					t.Fatalf("rolled-back binding: %+v %v", after, readErr)
				}
			}
			// sql-guarantee: The caller's rollback must also remove the independent version ledger; no owner reader lists it.
			dbtest.Count(t, db, wantVersions, `SELECT COUNT(*) FROM integration_versions`)
			receipts := integrationReceipts(t, db, "integration.saved", "account")
			wantReceipts := 0
			if scenario == "success" {
				wantReceipts = 1
			}
			if len(receipts) != wantReceipts || called {
				t.Fatalf("receipts=%+v postcommit hook=%v", receipts, called)
			}
		})
	}
}
