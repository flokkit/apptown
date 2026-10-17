package modelcall

import "testing"

// Retries of one question add up, and one incomplete call leaves the sum
// incomplete.
func TestUsageAddKeepsIncompleteness(t *testing.T) {
	t.Parallel()
	u := &Usage{Prompt: 1, Completion: 1, Cost: 0.5}
	u.Add(&Usage{Prompt: 2, Cost: 0.25, Incomplete: "no usage"})
	if u.Prompt != 3 || u.Completion != 1 || u.Cost != 0.75 || u.Incomplete == "" {
		t.Fatalf("sum: %+v", u)
	}
}
