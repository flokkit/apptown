package docker

import "testing"

// No-stats rows are absent, as unreported containers are; malformed sizes
// still fail the read. A real zero is known and must remain present.
func TestRunningUsageSkipsOnlyExplicitNoStats(t *testing.T) {
	t.Parallel()
	scope := Scope{Client: &fakeClient{reply: func(Command) ([]byte, error) {
		return []byte("running\t12MiB / 1GiB\t2.00%\nstopped\t-- / --\t--\n--\t-- / --\t--\nempty\t0B / 1GiB\t0.00%\n"), nil
	}}}
	got, err := RunningUsage(t.Context(), scope)
	if err != nil || len(got) != 2 || got["running"].MemoryBytes != 12<<20 {
		t.Fatalf("no-stats row failed other containers: %v %v", got, err)
	}
	if value, ok := got["empty"]; !ok || value.MemoryBytes != 0 {
		t.Fatalf("real zero was omitted: %v", got)
	}
	if _, err := parseUsage([]byte("broken\tnonsense / 1GiB\t1%\n")); err == nil {
		t.Fatal("malformed usage became zero")
	}
}

// A container's CPU reads as cores (150% is 1.5 cores) beside its memory;
// a no-stats row is absent and an unreadable CPU fails the read, never zero.
func TestContainerUsageReadsCPUAsCores(t *testing.T) {
	t.Parallel()
	scope := Scope{Client: &fakeClient{reply: func(Command) ([]byte, error) {
		return []byte("web\t12MiB / 1GiB\t150.00%\nstopped\t-- / --\t--\nidle\t0B / 1GiB\t0.00%\n"), nil
	}}}
	got, err := ContainerUsage(t.Context(), scope, []string{"web", "stopped", "idle"})
	if err != nil || len(got) != 2 {
		t.Fatalf("usage: %v %v", got, err)
	}
	if got["web"] != (Usage{MemoryBytes: 12 << 20, CPUCores: 1.5}) {
		t.Fatalf("web: %+v", got["web"])
	}
	if u, ok := got["idle"]; !ok || u.CPUCores != 0 {
		t.Fatalf("real zero CPU was omitted: %v", got)
	}
	if _, err := parseUsage([]byte("web\t1MiB / 1GiB\tnonsense\n")); err == nil {
		t.Fatal("malformed CPU became zero")
	}
}
