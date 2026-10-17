package clocktest

import (
	"testing"
	"time"
)

// Each reading moves the clock on by its step, and Next shows the coming
// reading without taking it.
func TestSteppingMovesOnAtEveryReading(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	c := NewStepping(start, time.Second)
	for range 2 {
		if !c.Next().Equal(start) {
			t.Fatal("Next took a reading")
		}
	}
	if a, b := c.Now(), c.Now(); !a.Equal(start) || !b.Equal(start.Add(time.Second)) {
		t.Fatalf("readings %v %v", a, b)
	}
}
