package database

import (
	"strconv"
	"strings"
	"testing"
)

// AppTown's own database is one of the supported majors; any other is
// refused naming the range, wherever the server came from.
//
// Kills: a server outside the range accepted; a refusal that does not say
// what AppTown needs.
func TestOwnServerIsOneOfTheSupportedMajors(t *testing.T) {
	t.Parallel()
	for major := ownPostgresMin; major <= OwnPostgresMax; major++ {
		if err := OwnServerSupported(major*10000 + 15); err != nil {
			t.Fatalf("PostgreSQL %d: %v", major, err)
		}
	}
	for _, major := range []int{ownPostgresMin - 1, OwnPostgresMax + 1} {
		err := OwnServerSupported(major*10000 + 2)
		if err == nil || !strings.Contains(err.Error(), "AppTown needs PostgreSQL") || !strings.HasSuffix(err.Error(), "this server is "+strconv.Itoa(major)) {
			t.Fatalf("PostgreSQL %d: %v", major, err)
		}
	}
}
