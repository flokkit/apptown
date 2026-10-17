package dataprotection

import (
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/dataprotection/internal/protectionsql"
	"flokkit.io/forge/internal/ids/idstest"
)

// A set's items are what a restore puts back: items that cannot be read
// are an error, never an empty set; a set recorded with none is empty.
func TestASetWhoseItemsCannotBeReadIsAnError(t *testing.T) {
	t.Parallel()
	if _, err := backupRecord(protectionsql.Backup{ID: idstest.N(7), Status: "ok", ItemsJson: []byte(`{"not":"a list"}`)}, nil); err == nil || !strings.Contains(err.Error(), "items could not be read") {
		t.Fatalf("unreadable items: %v", err)
	}
	b, err := backupRecord(protectionsql.Backup{ID: idstest.N(8), Status: "ok", ItemsJson: []byte(`[]`)}, nil)
	if err != nil || b.Items == nil || len(b.Items) != 0 {
		t.Fatalf("an empty set: %+v %v", b.Items, err)
	}
}
