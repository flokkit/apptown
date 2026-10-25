package strs

import "testing"

func TestDisplayLabelsRemainPresentationText(t *testing.T) {
	for _, s := range []string{"Customer services", "客户环境", "Production — Sydney", "a/b"} {
		if got, err := DisplayLabel("  " + s + "  "); err != nil || got != s {
			t.Fatalf("label %q: %q %v", s, got, err)
		}
	}
	for _, s := range []string{"", "  ", "hidden\nline", "bad\x00value", string([]byte{0xff})} {
		if _, err := DisplayLabel(s); err == nil {
			t.Fatalf("invalid label accepted: %q", s)
		}
	}
}
