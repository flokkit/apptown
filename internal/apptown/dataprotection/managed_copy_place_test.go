package dataprotection

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A check's new database is named within DigitalOcean's 40 characters,
// whatever the database's name; two operations' places differ.
func TestACheckPlaceFitsTheProvidersNameLimit(t *testing.T) {
	t.Parallel()
	for _, db := range []string{"app", strings.Repeat("ledger", 10)} {
		a, b := checkPlace(db, idstest.N(1)), checkPlace(db, idstest.N(2))
		if len(a) > 40 || a == b || !strings.Contains(a, "_check_") {
			t.Fatalf("%s: %q %q", db, a, b)
		}
	}
}
