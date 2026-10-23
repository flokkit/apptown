package workflowexec

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/database"
)

// selectionWithDefault is model selection on db whose default slot names
// pattern, as on an AppTown where an administrator chose a default model.
func selectionWithDefault(t *testing.T, db *database.DB, pattern string) model.Selection {
	t.Helper()
	slots := model.New(db)
	if err := slots.SetDefault(context.Background(), pattern); err != nil {
		t.Fatal(err)
	}
	return model.NewSelection(slots)
}
