package enum

import (
	"errors"
	"testing"
)

type color uint8

var errColor = errors.New("invalid color")

var colors = Set[color]{Names: []string{"red", "green"}, Invalid: "invalid-color", Err: errColor, ScanLabel: "value"}

// Every value round-trips through its word in storage and JSON, and anything
// outside the set is refused with the type's own error, leaving the target as
// it was.
func TestSetCodecRoundTripsWordsAndRefusesEverythingElse(t *testing.T) {
	for v, word := range map[color]string{1: "red", 2: "green"} {
		if colors.String(v) != word {
			t.Fatalf("String(%d) = %q", v, colors.String(v))
		}
		stored, err := colors.Value(v)
		if err != nil || stored != word {
			t.Fatalf("Value(%d) = %v %v", v, stored, err)
		}
		var scanned color
		if err := colors.Scan(&scanned, []byte(word)); err != nil || scanned != v {
			t.Fatalf("Scan(%q) = %d %v", word, scanned, err)
		}
		body, err := colors.JSON(v)
		if err != nil || string(body) != `"`+word+`"` {
			t.Fatalf("JSON(%d) = %s %v", v, body, err)
		}
		var decoded color
		if err := colors.ReadJSON(&decoded, body); err != nil || decoded != v {
			t.Fatalf("ReadJSON(%s) = %d %v", body, decoded, err)
		}
	}
	for _, v := range []color{0, 3} {
		if colors.String(v) != "invalid-color" || colors.valid(v) {
			t.Fatalf("value %d must be invalid", v)
		}
		if _, err := colors.Value(v); !errors.Is(err, errColor) || err.Error() != "invalid color: "+map[color]string{0: "0", 3: "3"}[v] {
			t.Fatalf("Value(%d) error: %v", v, err)
		}
		if _, err := colors.JSON(v); !errors.Is(err, errColor) {
			t.Fatalf("JSON(%d) error: %v", v, err)
		}
	}
	kept := color(2)
	if err := colors.Scan(&kept, "blue"); !errors.Is(err, errColor) || err.Error() != `invalid color: "blue"` || kept != 2 {
		t.Fatalf("unknown word: %v, target %d", err, kept)
	}
	if err := colors.Scan(&kept, nil); !errors.Is(err, errColor) || err.Error() != "invalid color: value <nil>" || kept != 2 {
		t.Fatalf("NULL: %v, target %d", err, kept)
	}
	if err := colors.Scan(&kept, int64(1)); err == nil || err.Error() != "invalid color: value int64" {
		t.Fatalf("non-text: %v", err)
	}
	if err := colors.ReadJSON(&kept, []byte(`1`)); err == nil || kept != 2 {
		t.Fatalf("a JSON number is not a word: %v", err)
	}
	if _, err := colors.Parse(""); !errors.Is(err, errColor) {
		t.Fatalf("empty word: %v", err)
	}
}
