package changeconsole

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
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

// The title can be a shortened chat label. The writer needs the actual ask,
// including follow-up requests, even when the change has no written brief.
func TestChatRequestReachesNarrativeWithEmptyBody(t *testing.T) {
	t.Parallel()
	request := "Ok so can we have a colour slider, glowing circle with tap to open, that lets you change the water colour please?"
	extra := ChatReports([]console.Conversation{{Ended: true, Messages: []console.Message{
		{Role: "you", Text: request, At: "2026-11-01T00:00:00.000Z"},
		{Role: "helper", Text: "Done"},
		{Role: "you", Text: "Use a softer glow.", At: "2026-11-01T00:00:02.000Z"},
	}}, {Messages: []console.Message{
		{Role: "you", Text: "Make the circle blue too.", At: "2026-11-01T00:00:01.000Z"},
	}}})
	got := workflow.NarrativeEvidence(workflow.NarrativeSource{Change: &workflow.Change{Title: "Ok so can we have a colour slider, glowing circle with tap to"}}, extra)
	if !strings.Contains(got, request) || !strings.Contains(got, "Make the circle blue too.") || strings.Index(got, request) > strings.Index(got, "Make the circle blue too.") || !strings.Contains(got, "Make the circle blue too.\nUse a softer glow.") {
		t.Fatalf("writer lost or reordered the request: %s", got)
	}
}

func TestChatRequestBoundsWholeMessagesAndExplainsOmissions(t *testing.T) {
	t.Parallel()
	got := chatRequest([]console.Message{
		{Role: "you", Text: "First request."},
		{Role: "you", Text: strings.Repeat("oversized ", 12_001)},
		{Role: "you", Text: "Last request."},
	})
	if strings.Contains(got, "oversized") || !strings.Contains(got, "1 whole messages omitted") || !strings.Contains(got, "Writer input bound") || !strings.Contains(got, "First request.\nLast request.") {
		t.Fatalf("request bound: %s", got)
	}
}
