package main

import "testing"

// TestChangeStateWordsSayHowAClosedChangeClosed: the change page's state
// words come from the recorded closedAs, never from a reason's text.
func TestChangeStateWordsSayHowAClosedChangeClosed(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ state, closedAs, want string }{
		{"shipped", "", "shipped"},
		{"shipped", "shipped-with", "shipped with a later change"},
		{"abandoned", "nothing-to-change", "closed: nothing to change"},
		{"abandoned", "", "abandoned"},
	} {
		if got := changeStateWords(c.state, c.closedAs); got != c.want {
			t.Errorf("%s/%q: %q, want %q", c.state, c.closedAs, got, c.want)
		}
	}
}
