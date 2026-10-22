package apptown

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/project"
)

// The prompt's list of permitted mutations is generated from the carve-outs
// GETOnly enforces: every verb the port lets through is named for the chat
// kind that may use it, and nothing else is promised.
func TestConsoleSteeringMutationsMatchGETOnly(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	p := &project.Project{Slug: "p", Name: "P"}
	desk := a.briefing.Console(t.Context(), p, "")
	shop := a.briefing.Workshop(t.Context(), p, "")
	for _, c := range consoleCarveOuts {
		switch c.scope {
		case "project":
			if !strings.Contains(desk, c.verb) || strings.Contains(shop, "`"+c.verb+"`") {
				t.Errorf("%s is a project-chat verb: desk=%v shop=%v", c.verb, strings.Contains(desk, c.verb), strings.Contains(shop, c.verb))
			}
		case "change":
			if strings.Contains(desk, c.verb) || !strings.Contains(shop, c.verb) {
				t.Errorf("%s is a change-chat verb: desk=%v shop=%v", c.verb, strings.Contains(desk, c.verb), strings.Contains(shop, c.verb))
			}
		default:
			if !strings.Contains(desk, c.verb) || !strings.Contains(shop, c.verb) {
				t.Errorf("%s is for both chats: desk=%v shop=%v", c.verb, strings.Contains(desk, c.verb), strings.Contains(shop, c.verb))
			}
		}
	}
}
