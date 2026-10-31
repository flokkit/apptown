package docker

import "testing"

// No-stats rows are absent, as unreported containers are; malformed sizes
// still fail the read. A real zero is known and must remain present.
func TestMemoryUsageSkipsOnlyExplicitNoStats(t *testing.T) {
	t.Parallel()
	scope := Scope{Client: &fakeClient{reply: func(Command) ([]byte, error) {
		return []byte("running\t12MiB / 1GiB\nstopped\t-- / --\n--\t-- / --\nempty\t0B / 1GiB\n"), nil
	}}}
	for _, read := range []func() (map[string]int64, error){
		func() (map[string]int64, error) { return RunningMemoryUsage(t.Context(), scope) },
		func() (map[string]int64, error) {
			return MemoryUsage(t.Context(), scope, []string{"running", "stopped", "empty"})
		},
	} {
		got, err := read()
		if err != nil || len(got) != 2 || got["running"] != 12<<20 {
			t.Fatalf("no-stats row failed other containers: %v %v", got, err)
		}
		if value, ok := got["empty"]; !ok || value != 0 {
			t.Fatalf("real zero was omitted: %v", got)
		}
	}
	if _, err := parseMemoryUsage([]byte("broken\tnonsense / 1GiB\n")); err == nil {
		t.Fatal("malformed usage became zero")
	}
}
