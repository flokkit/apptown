package deployment

import (
	"errors"
	"testing"
)

func TestNeedWords(t *testing.T) {
	t.Parallel()
	var n NeedState
	for _, word := range []string{"creating", "ready", "failed", "orphaned"} {
		if err := n.Scan(word); err != nil || n.String() != word {
			t.Fatalf("need scan %q: %s %v", word, n, err)
		}
	}
	if err := n.Scan("done"); !errors.Is(err, ErrNeedState) {
		t.Fatalf("an unknown need word is refused: %v", err)
	}
}
