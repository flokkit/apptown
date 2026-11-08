package hostmem

import (
	"math"
	"testing"
)

func TestCPUCountsBusyCoresWithoutIdleIOWaitOrDuplicateGuestTime(t *testing.T) {
	t.Parallel()
	a, err := ParseStat([]byte("cpu 100 10 20 400 50 3 4 5 20 2\ncpu0 1 0 0 0 0 0 0 0\ncpu1 1 0 0 0 0 0 0 0\nintr 42\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseStat([]byte("cpu 130 10 30 440 60 3 4 15 30 3\ncpu0 1 0 0 0 0 0 0 0\ncpu1 1 0 0 0 0 0 0 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	used, err := UsedCores(a, b)
	if err != nil || math.Abs(used-1) > 1e-10 {
		t.Fatalf("busy cores = %v, %v", used, err)
	}
	idle := a
	idle.Counters[3] += 100
	if n, err := UsedCores(a, idle); err != nil || n != 0 {
		t.Fatalf("idle = %v, %v", n, err)
	}
	for _, bad := range []CPU{a, {Cores: 1}, {Cores: 2}} {
		if _, err := UsedCores(a, bad); err == nil {
			t.Errorf("accepted invalid sample %+v", bad)
		}
	}
}

func TestMalformedStatHasNoCPUReading(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"", "cpu 1 2 3\ncpu0 0\n", "cpu 1 2 3 4 5 6 7 x\ncpu0 0\n", "cpu -1 0 0 0 0 0 0 0\ncpu0 0\n", "cpu 18446744073709551615 1 0 0 0 0 0 0\ncpu0 0\n", "cpu 1 0 0 0 0 0 0 0\n", "cpu0 1 0 0 0 0 0 0 0\n", "cpu 1 0 0 0 0 0 0 0\ncpu0 0\ncpu0 0\n"} {
		if c, err := ParseStat([]byte(s)); err == nil {
			t.Errorf("%q accepted: %+v", s, c)
		}
	}
}
