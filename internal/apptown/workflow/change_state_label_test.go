package workflow

import "testing"

// One rule for a change's state words, read by the CLI and the web alike:
// how a closed change was closed decides what more there is to say.
func TestChangeStateLabel(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		state    ChangeState
		closedAs string
		want     string
	}{
		{ChangeInProgress, "", "in progress"},
		{ChangeLanding, "", "landing"},
		{ChangeShipped, "", "shipped"},
		{ChangeShipped, ClosedShippedWith, "shipped with a later change"},
		{ChangeShipped, closedRecord, "finished"},
		{ChangeAbandoned, "", "abandoned"},
		{ChangeAbandoned, ClosedNothingToChange, "closed: nothing to change"},
	} {
		if got := c.state.Label(c.closedAs); got != c.want {
			t.Errorf("%s/%q: %q, want %q", c.state, c.closedAs, got, c.want)
		}
	}
}
