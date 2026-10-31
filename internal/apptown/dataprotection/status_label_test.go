package dataprotection

import (
	"encoding/json"
	"github.com/flokkit/apptown/internal/apptown/dataprotection/internal/protectionsql"
	"testing"
)

func TestBackupLabelsKeepPartialFailuresVisible(t *testing.T) {
	for _, tc := range []struct {
		state BackupState
		want  BackupLabel
	}{{BackupOK, LabelFinished}, {BackupPartial, LabelFailed}, {BackupFailed, LabelFailed}, {BackupPruned, LabelFinished}} {
		b, err := backupRecord(protectionsql.Backup{Status: tc.state.String()}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if b.StatusLabel != tc.want {
			t.Fatalf("%s: %s", tc.state, b.StatusLabel)
		}
		raw, err := json.Marshal(ProjectBackup{Backup: b, Project: "lunch"})
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Project     string
			StatusLabel BackupLabel
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.Project != "lunch" || got.StatusLabel != tc.want {
			t.Fatalf("projection lost facts: %s", raw)
		}
	}
}

// Removing an old copy is retention, not an interrupted backup job.
func TestPrunedBackupIsFinished(t *testing.T) {
	if got := (Backup{Status: BackupPruned}).label().String(); got != "finished" {
		t.Fatalf("pruned backup reads as %q; want finished", got)
	}
}
