package scm

import (
	"testing"

	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// A scratch ref names its step and attempt exactly, though a step's id may
// itself contain "-a" (review IC-C01): a misread ref would let the sweep
// delete a live step's work.
func TestScratchRefReadsBackItsStepAndAttempt(t *testing.T) {
	t.Parallel()
	change := idstest.Parse(t, "0193f7a2-6b1c-7000-8000-000000000001")
	for _, step := range []ids.ID{idstest.Parse(t, "0193f7a2-6b1c-7000-a000-1234567890ab"), idstest.Parse(t, "0193f7a2-6b1c-7a00-8000-0000000000aa")} {
		got, attempt, ok := parseScratchRef(ScratchRef(change, step, 12))
		if !ok || got != step || attempt != 12 {
			t.Fatalf("%s: read %s attempt %d (%v)", step, got, attempt, ok)
		}
	}
	for _, bad := range []string{ScratchPrefix + "change-x/step-0193f7a2-6b1c-7000-a000-1234567890ab", ScratchPrefix + "change-x/step-0193f7a2-a1"} {
		if _, _, ok := parseScratchRef(bad); ok {
			t.Errorf("read %q", bad)
		}
	}
}
