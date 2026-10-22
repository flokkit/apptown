package integration

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/integration/internal/integrationsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

func TestRemoveProjectInKeepsOtherProjects(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	first := createTestProject(t, db, "first", "First")
	second := createTestProject(t, db, "second", "Second")
	attachments := NewAttachments(db)
	for _, pid := range []ids.ID{first, second} {
		if err := attachments.Attach(t.Context(), pid, "errors", "glitchtip", `{}`, Entered, "tester", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error { return RemoveProjectIn(context.Background(), tx, first) }); err != nil {
		t.Fatal(err)
	}
	// The stored row, not Attached: Attached treats a row without provenance
	// as no attachment, so a row the removal left behind could hide there.
	rows := integrationsql.New(db.SQL())
	if _, err := rows.ProjectIntegration(context.Background(), integrationsql.ProjectIntegrationParams{ProjectID: first, Capability: "errors"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("removed project's integration row survived: %v", err)
	}
	if _, err := rows.ProjectIntegration(context.Background(), integrationsql.ProjectIntegrationParams{ProjectID: second, Capability: "errors"}); err != nil {
		t.Fatalf("other project's integration row: %v", err)
	}
}
