package deployment

import (
	"errors"
	"testing"
)

func TestNeedAndReconciliationWords(t *testing.T) {
	t.Parallel()
	var n NeedState
	for _, word := range []string{"pending", "creating", "ready", "failed", "orphaned"} {
		if err := n.Scan(word); err != nil || n.String() != word {
			t.Fatalf("need scan %q: %s %v", word, n, err)
		}
	}
	if err := n.Scan("done"); !errors.Is(err, ErrNeedState) {
		t.Fatalf("an unknown need word is refused: %v", err)
	}
	if got, err := ReconciliationRunning.Ended(false); err != nil || got != ReconciliationDone {
		t.Fatalf("a pass ends done: %s %v", got, err)
	}
	if got, err := ReconciliationRunning.Ended(true); err != nil || got != ReconciliationFailed {
		t.Fatalf("a pass ends failed: %s %v", got, err)
	}
	for _, ended := range []ReconciliationState{ReconciliationDone, ReconciliationFailed} {
		if _, err := ended.Ended(false); !errors.Is(err, ErrReconciliationTransition) {
			t.Fatalf("%s ends once: %v", ended, err)
		}
	}
	var zero ReconciliationState
	if _, err := zero.Value(); !errors.Is(err, ErrReconciliationState) {
		t.Fatalf("the zero value cannot be stored: %v", err)
	}
}
