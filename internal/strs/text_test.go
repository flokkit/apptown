package strs

import (
	"testing"
	"unicode/utf8"
)

func TestTruncateKeepsShortTextAndCutsLongTextWithAnEllipsis(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"", 5, ""},
		{"hello", 5, "hello"},
		{"hello world", 5, "hello…"},
		{"hello", 0, "…"},
		{"hello", -1, "…"},
	}
	for _, c := range cases {
		if got := Truncate(c.in, c.n); got != c.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestTruncateNeverSplitsAMultiByteCharacter(t *testing.T) {
	// "é" is two bytes and "日" is three: every cut point inside them must back
	// off to the character's start.
	in := "aé日b"
	for n := 0; n <= len(in); n++ {
		got := Truncate(in, n)
		if !utf8.ValidString(got) {
			t.Fatalf("Truncate(%q, %d) = %q is not valid UTF-8", in, n, got)
		}
	}
	if got := Truncate(in, 2); got != "a…" {
		t.Fatalf("cut inside é: got %q, want %q", got, "a…")
	}
	if got := Truncate(in, 5); got != "aé…" {
		t.Fatalf("cut inside 日: got %q, want %q", got, "aé…")
	}
}

// A command's error is at the end of its output: Tail keeps the end, says
// how much came before, and never starts inside a character.
func TestTailKeepsTheEndAndCountsWhatItOmits(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"", 5, ""},
		{"hello", 5, "hello"},
		{"progress\nError: boom", 11, "…(9 earlier characters omitted)\nError: boom"},
		{"aé日b", 4, "…(2 earlier characters omitted)\n日b"},
		{"aé日b", 3, "…(3 earlier characters omitted)\nb"},
		{"hello", -1, "…(5 earlier characters omitted)\n"},
	}
	for _, c := range cases {
		got := Tail(c.in, c.n)
		if got != c.want {
			t.Errorf("Tail(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("Tail(%q, %d) = %q is not valid UTF-8", c.in, c.n, got)
		}
	}
}

func TestFirstNonBlankTreatsWhitespaceAsBlank(t *testing.T) {
	if got := FirstNonBlank("", "  ", "\n", "x", "y"); got != "x" {
		t.Fatalf("got %q, want x", got)
	}
	if got := FirstNonBlank("", " "); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	if got := FirstNonBlank(" a "); got != " a " {
		t.Fatalf("the chosen value is returned as given, got %q", got)
	}
}

func TestShortSHAHasOneLengthAndOneRenderingForNoCommit(t *testing.T) {
	for in, want := range map[string]string{
		"0123456789abcdef0123456789abcdef01234567": "0123456789ab",
		" 0123456789abcdef\n":                      "0123456789ab",
		"abc":                                      "abc",
		"":                                         "(none)",
		"  ":                                       "(none)",
	} {
		if got := ShortSHA(in); got != want {
			t.Errorf("ShortSHA(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJSONReportsAValueItCannotEncode(t *testing.T) {
	if got, err := JSON(map[string]int{"a": 1}); err != nil || got != `{"a":1}` {
		t.Fatalf("JSON = %q, %v", got, err)
	}
	if got, err := JSON(map[string]any{"f": func() {}}); err == nil || got != "" {
		t.Fatalf("an unencodable value gave %q, %v; want an error and no stand-in text", got, err)
	}
}
