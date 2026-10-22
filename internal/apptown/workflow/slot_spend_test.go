package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
)

// Slot spend is derived from completed step receipts and ignores receipts
// outside the requested reporting window.
func TestSlotSpendInUsesRecentModelReceipts(t *testing.T) {
	t.Parallel()
	db, run, _ := completionFixture(t, "working")
	recentID := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "work", Name: "do", Type: "agent/pi", Status: StatusPending.String()})
	changed, err := workflowsql.New(db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), Status: StatusPass.String(), EvidenceJson: []byte(`{"modelSlot":"plan","usage":{"input":100,"output":40}}`), ID: recentID, Attempt: 0, ExpectedStatus: StatusPending.String()})
	if err != nil || changed != 1 {
		t.Fatalf("store recent model receipt: changed=%d %v", changed, err)
	}
	// sql-guarantee: the reporting-window law requires a completed receipt at a fixed 2020 finished_at; RecordApproval always stamps the current database time.
	completionInsert(t, db, `INSERT INTO steps(id,run_id,stage,name,type,status,config_json,evidence_json,finished_at)
		VALUES (?,?,'work','old','agent/pi','pass','{}',?::jsonb,?)`, run.ID,
		`{"modelSlot":"plan","usage":{"input":999,"output":999}}`, time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC))
	spend, err := SlotSpendIn(context.Background(), db, time.Now().Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if spend["plan"]["input"] != 100 || spend["plan"]["output"] != 40 {
		t.Fatalf("spend must sum only in-window receipts: %+v", spend["plan"])
	}
}
