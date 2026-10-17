package changeconsole

import (
	"fmt"
	"strings"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/strs"
)

// ChatReports is what a change's chats tell its narrative writer: from each
// chat, its newest two substantial helper messages (over 120 characters),
// newest first. Chat reports can name deviations absent from artifacts.
func ChatReports(chats []*console.Session) string {
	var evidence strings.Builder
	for _, session := range chats {
		evidence.WriteString(chatReport(session.Messages()))
	}
	return evidence.String()
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
