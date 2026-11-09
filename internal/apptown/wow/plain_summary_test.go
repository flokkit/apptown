package wow

import (
	"strings"
	"testing"
)

func TestPlainSummary(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, in, want string }{
		{"links keep their text", "Read [the guide](https://x.test/g) and **follow** it.", "Read the guide and follow it."},
		{"fenced code is skipped", "```yaml\napp: web\n```\n\nPlain words here.", "Plain words here."},
		{"tilde fences too", "~~~\ncode\n~~~\nAfter the fence.", "After the fence."},
		{"indented code is skipped", "    app:\n      web: {}\n\nThen prose.", "Then prose."},
		{"multi-line comments are skipped", "<!--\nnote to self\n-->\nThe rule.", "The rule."},
		{"tables are skipped", "| a | b |\n|---|---|\n| 1 | 2 |\n\nAfter the table.", "After the table."},
		{"headings are skipped", "# Title\n\nThe first paragraph\nruns on.\n\nThe second.", "The first paragraph runs on."},
		{"snake case survives", "Trust `APPTOWN_ORIGINS` and _this_.", "Trust APPTOWN_ORIGINS and this."},
		{"no prose gives the heading", "# **The** title\n## Part", "The title"},
		{"nothing gives nothing", "```\ncode\n```", ""},
	} {
		if got := PlainSummary(c.in); got != c.want {
			t.Errorf("%s: PlainSummary(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
	long := PlainSummary(strings.Repeat("word ", 100))
	if n := len([]rune(long)); n >= 200 || !strings.HasSuffix(long, "word…") {
		t.Errorf("a long paragraph is cut at a word under 200 characters: %d %q", n, long)
	}
}
