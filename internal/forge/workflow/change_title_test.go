package workflow

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChangeTitleOwnsItsStatesAndLegalActs(t *testing.T) {
	t.Parallel()
	for _, owner := range []titleOwner{titleHuman, titleAwaitingChat, titleGenerated} {
		value, err := owner.Value()
		if err != nil {
			t.Fatal(err)
		}
		for _, input := range []any{value, []byte(value.(string))} {
			var decoded titleOwner
			if err := decoded.Scan(input); err != nil || decoded != owner {
				t.Fatalf("codec: %v %v", decoded, err)
			}
		}
		old := changeTitle{"before", owner}
		for _, act := range []titleAct{pinTitle, nameFromFirstMessage, reviseTitle} {
			next, applied, err := old.decide(changeTitleEdit{act, "new words", "before"})
			wantApplied := act == pinTitle || act == nameFromFirstMessage && owner == titleAwaitingChat || act == reviseTitle && owner == titleGenerated
			if err != nil || applied != wantApplied {
				t.Fatalf("act %d owner %d: %+v %v %v", act, owner, next, applied, err)
			}
			if applied {
				wantOwner := titleGenerated
				if act == pinTitle {
					wantOwner = titleHuman
				}
				if next.words != "new words" || next.owner != wantOwner {
					t.Fatalf("decision: %+v", next)
				}
			} else if next != old {
				t.Fatalf("refused act changed title: %+v", next)
			}
		}
		if _, applied, err := old.decide(changeTitleEdit{act: pinTitle, words: " \n "}); err != nil || applied {
			t.Fatal("blank title pinned")
		}
	}
	for _, raw := range []any{nil, 42, "", "unknown", "pending"} {
		var owner titleOwner
		if err := owner.Scan(raw); !errors.Is(err, errChangeTitle) {
			t.Fatalf("accepted unknown %v: %v", raw, err)
		}
	}
	if _, err := (titleOwner(0)).Value(); !errors.Is(err, errChangeTitle) {
		t.Fatal("zero is not a title state")
	}
	if _, _, err := (changeTitle{"words", 0}).decide(changeTitleEdit{act: pinTitle, words: "new"}); !errors.Is(err, errChangeTitle) {
		t.Fatal("invalid state admitted")
	}
	if _, _, err := (changeTitle{"words", titleHuman}).decide(changeTitleEdit{}); !errors.Is(err, errChangeTitle) {
		t.Fatal("invalid act admitted")
	}
	if _, applied, err := (changeTitle{"newer", titleGenerated}).decide(changeTitleEdit{reviseTitle, "old result", "earlier"}); err != nil || applied {
		t.Fatal("old model observation changed new words")
	}
	title := firstMessageTitle(strings.Repeat("界", 100))
	if !utf8.ValidString(title) || len(title) > 64 || !strings.HasSuffix(title, "…") {
		t.Fatalf("first message byte bound: %q", title)
	}
	if got := firstMessageTitle("  Build\n a game. Then add music. "); got != "Build a game." {
		t.Fatalf("first sentence: %q", got)
	}
}
