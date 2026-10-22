package wow

import (
	"context"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/wow/internal/wowsql"
	"github.com/flokkit/apptown/internal/database"
)

// Test observations through the library's own reads and the audit ledger,
// so a renamed column breaks compilation instead of an assertion.

func libraryRows(q database.Queryer) *wowsql.Queries { return wowsql.New(q.SQL()) }

// wowReceipts is every ledger line whose kind starts with prefix and whose
// subject is subject (any subject when empty). The read is bounded, so it
// refuses to answer when the bound is reached.
func wowReceipts(t *testing.T, q database.Queryer, prefix, subject string) []audit.Entry {
	t.Helper()
	all, err := audit.Recent(context.Background(), q, audit.Filter{KindPrefix: prefix, Subject: subject, Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == audit.MaxLimit {
		t.Fatalf("%d or more %q receipts: a bounded read cannot count them", audit.MaxLimit, prefix)
	}
	var out []audit.Entry
	for _, e := range all {
		if strings.HasPrefix(e.Kind, prefix) {
			out = append(out, e)
		}
	}
	return out
}
