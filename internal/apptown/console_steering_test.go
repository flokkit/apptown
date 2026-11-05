package apptown

import (
	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
)

// The prompt's list of permitted mutations is generated from the carve-outs
// GETOnly enforces: every verb the port lets through is named for the chat
// kind that may use it, and nothing else is promised.
func TestConsoleSteeringMutationsMatchGETOnly(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	p := &project.Project{Slug: "p", Name: "P"}
	desk := a.briefing.Console(t.Context(), p, "", adoption.Resolved{})
	shop := a.briefing.Workshop(t.Context(), p, "", adoption.Resolved{})
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

func TestFreshWritableChatReceivesDeclaredContextAtStartingCommit(t *testing.T) {
	t.Parallel()
	for _, present := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "present"}[present], func(t *testing.T) {
			t.Parallel()
			w := newHolderWorld(t)
			p, err := w.a.Projects.BySlug(asPerson("owner"), "game")
			if err != nil {
				t.Fatal(err)
			}
			hub, err := p.Repository(w.a.cfg.dataDir)
			if err != nil {
				t.Fatal(err)
			}
			b, err := w.a.Ways.CreateBlock(t.Context(), "rules/context", "<!-- context: [{\"path\":\"PROJECT-NOTES.md\",\"maxBytes\":6144}] -->\nKeep it plain.", "", "boss")
			if err != nil {
				t.Fatal(err)
			}
			if err := w.a.Adoption.AppendPlatformImport(t.Context(), p.ID, *b); err != nil {
				t.Fatal(err)
			}
			prior, err := w.a.Adoption.CurrentReceipt(t.Context(), p.ID)
			if err != nil {
				t.Fatal(err)
			}
			receipt := prior.Receipt
			receipt.Guidance = append(receipt.Guidance, adoption.ReceiptBlock{Name: b.Name, BlockID: b.ID, Version: 1})
			if err := w.a.DB.InTx(t.Context(), func(tx *database.Tx) error {
				return adoption.RecordIn(t.Context(), tx, p.ID, prior.Revision+1, adoption.AdoptionCreate, "declared context", receipt, "boss", time.Now())
			}); err != nil {
				t.Fatal(err)
			}
			const fact = "Distinctive accepted chat context."
			if present {
				commitIn(t, hub, "PROJECT-NOTES.md", "# Project notes\n- "+fact+" (agent · change 1)\n")
			}
			change, err := w.a.Work.Create(asPerson("owner"), workflow.ChangeDraft{ProjectID: p.ID, Title: "Use accepted context"})
			if err != nil {
				t.Fatal(err)
			}
			if err := w.a.Work.PrepareWorkspace(asPerson("owner"), change.ID); err != nil {
				t.Fatal(err)
			}
			base := gittest.Run(t, hub, "rev-parse", "HEAD")
			commitIn(t, hub, "PROJECT-NOTES.md", "later main context must not prime this change")
			s, err := w.a.consoleAdmission.Open(asPerson("owner"), &p, conversation.ProjectOpen{ChangeID: change.ID, Model: "fake/model"}, "owner")
			if err != nil {
				t.Fatal(err)
			}
			text := s.Steering()
			if strings.Contains(text, fact) != present || strings.Contains(text, "later main context") {
				t.Fatalf("wrong initial brief: %s", text)
			}
			if present && !strings.Contains(text, "commit "+base) {
				t.Fatal("missing selected commit")
			}
		})
	}
}
