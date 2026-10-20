package repositoryedit

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/gitexec/gittest"
)

// Export filters NonSourceIn through ReportArtifact over every carried
// commit. Renaming the tip cannot erase an older lane's committed report.
func TestAReportCommittedBeforeTheRenameStaysExportable(t *testing.T) {
	dir := t.TempDir()
	gittest.Run(t, dir, "init", "-q", "-b", "main")
	if err := os.Mkdir(filepath.Join(dir, ".forge"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".forge", "REPORT.md"), []byte("Historical build report\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, dir, "add", "-A")
	gittest.RunAsTester(t, dir, "commit", "-qm", "old lane report")
	gittest.Run(t, dir, "mv", ".forge", ".apptown")
	gittest.RunAsTester(t, dir, "commit", "-qm", "rename")
	found, err := scm.NonSourceIn(t.Context(), dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if refused := slices.DeleteFunc(found, ReportArtifact); len(refused) != 0 {
		t.Fatalf("export refuses historical prose as runtime credentials: %v", refused)
	}
}
