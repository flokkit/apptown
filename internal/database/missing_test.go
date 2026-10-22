package database

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/fault"
)

// A read by identity that finds nothing is "not found" and still matches
// sql.ErrNoRows for callers that ask; anything else passes through untouched.
func TestMissingClassifiesOnlyAnAbsentRow(t *testing.T) {
	err := Missing(fmt.Errorf("reading change 7: %w", sql.ErrNoRows))
	if fault.KindOf(err) != fault.NotFound || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("absent row: kind %v, is ErrNoRows %v", fault.KindOf(err), errors.Is(err, sql.ErrNoRows))
	}
	other := errors.New("connection reset")
	if Missing(other) != other || Missing(nil) != nil {
		t.Fatal("other errors and nil pass through")
	}
}
