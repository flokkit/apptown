package projectnotes

import (
	"strings"
	"testing"
)

func TestAttributedNotesAndInvalidLines(t *testing.T) {
	for _, tc := range []struct {
		body    []byte
		want    int
		errLine string
	}{
		{nil, 0, ""}, {[]byte("# Project notes\n- Real photos only. (Sam · change 4)\n- Run with mise. (agent · change 5)\n"), 2, ""},
		{[]byte("# Project notes\n- No attribution.\n"), 0, ":2:"},
		{[]byte("# Project notes\n- First line. (Sam · change 4)\n  continuation\n"), 0, ":3:"},
		{[]byte("# Project notes\n" + strings.Repeat("- Fact. (agent · change 1)\n", 41)), 0, ":42:"},
		{[]byte("# Project notes\n" + strings.Repeat("x", 6144)), 0, ":1:"},
	} {
		got, err := parse(tc.body)
		if tc.errLine != "" {
			if err == nil || !strings.Contains(err.Error(), tc.errLine) {
				t.Fatalf("%q: %v", tc.errLine, err)
			}
			continue
		}
		if err != nil || len(got) != tc.want {
			t.Fatalf("got %v %v", got, err)
		}
	}
}
func TestNotesDiffShowsActualLinesIncludingInvalidProposals(t *testing.T) {
	d := FromDiff("diff --git a/PROJECT-NOTES.md b/PROJECT-NOTES.md\n--- a/PROJECT-NOTES.md\n+++ b/PROJECT-NOTES.md\n@@ -2 +2 @@\n-- Old. (A · change 1)\n+- New. (agent · change 2)\n+malformed\n")
	if d == nil || len(d.Added) != 2 || len(d.Removed) != 1 || d.Added[1] != "malformed" {
		t.Fatalf("%+v", d)
	}
	if FromDiff("") != nil {
		t.Fatal("unchanged file must not show a row")
	}
}
