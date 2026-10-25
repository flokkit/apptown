package workflow

import "testing"

// requireLocation keeps error-bearing location reads explicit in test expressions.
func requireLocation(t testing.TB) func(string, error) string {
	t.Helper()
	return func(path string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
}
