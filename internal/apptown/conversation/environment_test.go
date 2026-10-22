package conversation

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/ids"
)

// Flipping the chat slot reaches existing chats: an unpinned session follows
// the slot at attach time; a session whose person named a model keeps it.
func TestUnpinnedChatFollowsTheSlot(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	slots := model.New(db)
	env := Environment{models: model.NewSelection(slots, "")}
	if err := slots.Set(context.Background(), "chat", ids.Nil, "", "standard", "openrouter/openai/gpt-5.6-luna", "fixture"); err != nil {
		t.Fatal(err)
	}
	born := &console.Session{Meta: console.Meta{Model: "anthropic/claude-opus-5"}}
	if got := env.chatModelFor(t.Context(), born); got != "openrouter/openai/gpt-5.6-luna" {
		t.Fatalf("an unpinned chat should follow the slot, got %q", got)
	}
	pinned := &console.Session{Meta: console.Meta{Model: "anthropic/claude-opus-5", ModelPinned: true}}
	if got := env.chatModelFor(t.Context(), pinned); got != "anthropic/claude-opus-5" {
		t.Fatalf("a pinned chat keeps its model, got %q", got)
	}
}
