package gitexec

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A normal edit to an existing file produces a thin incremental bundle.
// Its deltas must resolve against the source objects in quarantine.
func TestAResultEditingASourceFileIsImported(t *testing.T) {
	hub := repo(t)
	var b strings.Builder
	for i := range 2000 {
		fmt.Fprintf(&b, "line %04d: original project content\n", i)
	}
	original := b.String()
	if err := os.WriteFile(filepath.Join(hub, "large.txt"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, hub, "add", ".")
	mustGit(t, hub, "commit", "-qm", "source file")
	base := mustGit(t, hub, "rev-parse", "HEAD")
	edited := strings.Replace(original, "line 1000: original", "line 1000: changed", 1)
	result := sendAndWork(t, hub, base, func(clone string) {
		if err := os.WriteFile(filepath.Join(clone, "large.txt"), []byte(edited), 0600); err != nil {
			t.Fatal(err)
		}
		mustGit(t, clone, "add", ".")
		mustGit(t, clone, "commit", "-qm", "edit one line")
	})
	dir, tip, cleanup, err := Quarantine(context.Background(), hub, result, "work", "work", base)
	defer cleanup()
	if err != nil {
		t.Fatalf("valid worker edit refused: %v", err)
	}
	if got := mustGit(t, dir, "show", tip+":large.txt"); got != strings.TrimSpace(edited) {
		t.Fatal("edited content lost")
	}
}
