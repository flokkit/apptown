package changeconsole

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/console"
)

// A chat tells the narrative writer its newest two substantial helper
// messages, newest first; short helper lines and the person's own messages
// are not reports.
func TestChatReportsAreTheNewestTwoSubstantialHelperMessages(t *testing.T) {
	t.Parallel()
	long := func(tag string) string { return tag + " " + strings.Repeat("detail ", 30) }
	got := chatReport([]console.Message{
		{Role: "helper", Text: long("oldest")},
		{Role: "helper", Text: long("middle")},
		{Role: "you", Text: long("asked")},
		{Role: "helper", Text: "ok, done"},
		{Role: "helper", Text: long("newest")},
	})
	if strings.Count(got, "HELPER'S REPORT") != 2 || strings.Contains(got, "oldest") || strings.Contains(got, "asked") || strings.Contains(got, "ok, done") {
		t.Fatalf("reports: %q", got)
	}
	if strings.Index(got, "newest") > strings.Index(got, "middle") {
		t.Fatalf("reports are not newest first: %q", got)
	}
}
