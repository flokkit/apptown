package wow

import (
	"os"
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
		{"inline code keeps its marks and its text", "Trust `APPTOWN_ORIGINS` and _this_, not `__init__`.", "Trust `APPTOWN_ORIGINS` and this, not `__init__`."},
		{"a colon before a list takes the list", "Do this:\n\n- First thing\n  continued.\n- Second.\n\nLater prose.", "Do this: First thing continued; Second."},
		{"a colon before code ends the sentence", "Declare it in `app.yaml`:\n\n    app:\n      web: {}\n", "Declare it in `app.yaml`."},
		{"a colon at the end ends the sentence", "Spring the boring way:", "Spring the boring way."},
		{"no prose gives the heading", "# **The** title\n## Part", "The title"},
		{"nothing gives nothing", "```\ncode\n```", ""},
	} {
		if got := PlainSummary(c.in); got != c.want {
			t.Errorf("%s: PlainSummary(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
	if open := PlainSummary(strings.Repeat("word ", 35) + "`a long code span that runs past the limit`"); strings.Count(open, "`")%2 != 0 {
		t.Errorf("a cut never leaves a code span open: %q", open)
	}
	long := PlainSummary(strings.Repeat("word ", 100))
	if n := len([]rune(long)); n >= 200 || !strings.HasSuffix(long, "word…") {
		t.Errorf("a long paragraph is cut at a word under 200 characters: %d %q", n, long)
	}
}

// The stock guidance the summaries were garbled on: a code span kept, a
// list after a colon taken, a colon before code turned into a full stop.
func TestPlainSummaryOfStockGuidance(t *testing.T) {
	t.Parallel()
	body := func(name string) string {
		raw, err := os.ReadFile("../../../stock/guidance/" + name + ".md")
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.SplitN(string(raw), "\n---\n", 2)
		return parts[len(parts)-1]
	}
	for _, c := range []struct{ name, prefix string }{
		{"lang/go/errors-are-values", "Go error handling: Wrap with context: fmt.Errorf("},
		{"build/container-image", "The final stage of a `build: .` part runs as a non-root `USER`."},
		{"deploy/health-declared", "Every part AppTown serves from your build or image declares its readiness in `.apptown/app.yaml` (a `static:` part declares nothing)."},
	} {
		got := PlainSummary(body(c.name))
		if !strings.HasPrefix(got, c.prefix) || strings.HasSuffix(got, ":") {
			t.Errorf("%s: summary %q, want it to start %q and not end on a colon", c.name, got, c.prefix)
		}
	}
}
