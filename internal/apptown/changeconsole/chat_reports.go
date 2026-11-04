package changeconsole

import (
	"fmt"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/strs"
)

// ChatReports gives the narrative writer the person's whole messages in order,
// followed by each chat's newest two substantial helper reports (over 120
// characters). Reports can name deviations absent from artifacts.
func ChatReports(chats []console.Conversation) string {
	var evidence strings.Builder
	var messages []console.Message
	for _, session := range chats {
		transcript := session.Messages
		messages = append(messages, transcript...)
		evidence.WriteString(chatReport(transcript))
	}
	return chatRequest(messages) + evidence.String()
}

func chatReport(messages []console.Message) string {
	var evidence strings.Builder
	picked := 0
	for i := len(messages) - 1; i >= 0 && picked < 2; i-- {
		if messages[i].Role == "helper" && len(strings.TrimSpace(messages[i].Text)) > 120 {
			fmt.Fprintf(&evidence, "HELPER'S REPORT (from the chat, newest first):\n%s\n", strs.Truncate(messages[i].Text, 2200))
			picked++
		}
	}
	return evidence.String()
}

// Keep whole messages within the same 120 kB budget as narrative evidence.
// Omitting an oversized message is explicit: it is a writer input limit,
// never a claim that the person sent an incomplete request.
func chatRequest(messages []console.Message) string {
	const budget = 120_000
	// Sessions can overlap. Transcript timestamps use a fixed UTC format;
	// preserve transcript order for messages with the same timestamp.
	messages = slices.Clone(messages)
	slices.SortStableFunc(messages, func(a, b console.Message) int { return strings.Compare(a.At, b.At) })
	var request strings.Builder
	omitted := 0
	for _, message := range messages {
		if message.Role != "you" || strings.TrimSpace(message.Text) == "" {
			continue
		}
		if request.Len()+len(message.Text)+1 > budget {
			omitted++
			continue
		}
		request.WriteString(message.Text)
		request.WriteByte('\n')
	}
	if request.Len() == 0 && omitted == 0 {
		return ""
	}
	note := ""
	if omitted > 0 {
		note = fmt.Sprintf("Writer input bound: %d whole messages omitted to fit %d bytes. The person’s messages were not cut off; this is a limit on what you were given. Do not judge omitted requests as unmet.\n", omitted, budget)
	}
	return "PERSON'S CHAT REQUEST (messages in order; part of the ask):\n" + note + request.String()
}
