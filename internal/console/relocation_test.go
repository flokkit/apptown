package console

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type relocationAgent struct {
	Agent
	status Status
	err    error
}

func (a relocationAgent) Status(context.Context, string) (Status, error) { return a.status, a.err }
func layoutRecord(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, "console", "chat")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	raw := []byte(`{"id":"chat","scope":"project:one","checkout":"/before/change-one","ownsCheckout":true,"token":"keep-secret","lastUsed":"2026-10-25T00:00:00Z","future":{"precise":1234567890123456789}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestRelocationInventoryRefusesUnreadableAndMismatchedRecords(t *testing.T) {
	for _, body := range []string{"{broken", `{"id":"wrong"}`} {
		t.Run(body, func(t *testing.T) {
			root := t.TempDir()
			path := layoutRecord(t, root, MetaFile)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := RelocationRecords(root); err == nil {
				t.Fatal("silently omitted unreadable record")
			}
		})
	}
}
func TestRelocationRequiresAllRuntimeWritersToBeStopped(t *testing.T) {
	root := t.TempDir()
	layoutRecord(t, root, MetaFile)
	records, err := RelocationRecords(root)
	if err != nil || len(records) != 1 {
		t.Fatalf("inventory: %+v %v", records, err)
	}
	for _, state := range []Status{{Running: true}, {SidecarRunning: true}, {ScriptExists: true}, {Attached: true}} {
		if err := records[0].RefuseWriter(t.Context(), relocationAgent{status: state}); err == nil {
			t.Fatalf("ignored writer %+v", state)
		}
	}
	if err := records[0].RefuseWriter(t.Context(), relocationAgent{err: errors.New("provider unavailable")}); err == nil {
		t.Fatal("unknown runtime treated as stopped")
	}
	if err := records[0].RefuseWriter(t.Context(), relocationAgent{}); err != nil {
		t.Fatal(err)
	}
}
func TestCheckoutRelocationPreservesIdentityAndUnknownFields(t *testing.T) {
	for _, name := range []string{MetaFile} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := layoutRecord(t, root, name)
			records, err := RelocationRecords(root)
			if err != nil || len(records) != 1 {
				t.Fatalf("inventory: %+v %v", records, err)
			}
			edit, err := records[0].CheckoutEdit("/after/change-one")
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := edit.Apply(); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if string(fields["checkout"]) != `"/after/change-one"` || string(fields["token"]) != `"keep-secret"` || string(fields["ownsCheckout"]) != "true" {
				t.Fatalf("changed other ownership fields: %s", raw)
			}
			var future struct {
				Precise json.Number `json:"precise"`
			}
			if err := json.Unmarshal(fields["future"], &future); err != nil || future.Precise.String() != "1234567890123456789" {
				t.Fatalf("lost unknown field precision: %s %v", raw, err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("exposed record: %v %v", info, err)
			}
		})
	}
}
func TestCheckoutRelocationRejectsConcurrentRecordChange(t *testing.T) {
	root := t.TempDir()
	path := layoutRecord(t, root, MetaFile)
	records, err := RelocationRecords(root)
	if err != nil {
		t.Fatal(err)
	}
	edit, err := records[0].CheckoutEdit("/after/change-one")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(records[0].raw, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := edit.Apply(); err == nil {
		t.Fatal("overwrote a changed record")
	}
}

func TestRelocationLeavesEndedHistoryUnchanged(t *testing.T) {
	root := t.TempDir()
	path := layoutRecord(t, root, EndedFile)
	// Historical records are readable evidence of a settled session.
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	records, err := RelocationRecords(root)
	if err != nil || len(records) != 0 {
		t.Fatalf("history entered live inventory: %+v %v", records, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("history changed: %q %v", after, err)
	}
}

func TestRelocationRequiresEvidenceForDirectoriesWithoutLiveRecords(t *testing.T) {
	root := t.TempDir()
	orphan := filepath.Join(root, "console", "checkout-missing")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := RelocationRecords(root); err == nil {
		t.Fatal("unowned missing record hidden by checkout name")
	}
	path := layoutRecord(t, root, EndedFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	meta["checkout"], err = json.Marshal(orphan)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	records, err := RelocationRecords(root)
	if err != nil || len(records) != 0 {
		t.Fatalf("owned ended snapshot: %+v %v", records, err)
	}
	if err := os.WriteFile(path, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RelocationRecords(root); err == nil {
		t.Fatal("unreadable ended proof accepted")
	}
}
