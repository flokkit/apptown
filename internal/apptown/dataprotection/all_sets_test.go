package dataprotection

import (
	"context"
	"testing"
)

// Every set says whether its deployment is still recorded: a taken-down
// deployment's sets stay, every kind of them, and the Backups page opens
// the project's deployments for them rather than a deployment that is
// gone.
func TestAllSetsSayWhichDeploymentsAreGone(t *testing.T) {
	t.Parallel()
	w := heldOnAppPlatform(t, "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n")
	ctx := context.Background()
	for _, b := range []Backup{
		{ProjectID: w.project, Deployment: "prod", TakenAt: "2026-10-16T10:00:00Z", Trigger: "manual", Path: "p/prod/a", Status: BackupOK},
		{ProjectID: w.project, Deployment: "old", TakenAt: "2026-10-16T11:00:00Z", Trigger: "scheduled", Path: "p/old/b", Status: BackupOK},
		{ProjectID: w.project, Deployment: "old", TakenAt: "2026-10-16T12:00:00Z", Trigger: triggerTakeDown, Path: "p/old/c", Status: BackupOK},
	} {
		if _, err := w.s.backups.Record(ctx, &b, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	sets, err := w.s.AllSets(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	gone := map[string]bool{}
	for _, s := range sets {
		if s.ProjectID == w.project {
			gone[s.Path] = s.DeploymentRemoved
		}
	}
	if gone["p/prod/a"] || !gone["p/old/b"] || !gone["p/old/c"] || len(gone) != 3 {
		t.Fatalf("which sets' deployments are gone: %v", gone)
	}
}
