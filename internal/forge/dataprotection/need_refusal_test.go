package dataprotection

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids/idstest"
)

// A restore rehearsal of a release served before password declarations
// were withdrawn is refused with the catalogue's sentence, never verified.
func TestARehearsalRefusesAWithdrawnNeedDeclaration(t *testing.T) {
	w := newRehearsalWorld(t)
	repo := filepath.Join(w.s.dataDir, "workspaces", "project-ledger")
	legacy := strings.Replace(rehearsalContract, "version: \"16\"", "version: \"16\"\n    password: app", 1)
	if err := os.WriteFile(filepath.Join(repo, ".apptown/app.yaml"), []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "legacy password declaration"}} {
		cmd := gittest.Command(args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %s: %v", out, err)
		}
	}
	revision, err := releaseGit(context.Background(), repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	digest := releaseRepo + digestA
	claim := &claimedAttempt{db: w.db, project: w.project, run: idstest.N(8), step: idstest.N(80), at: 1}
	bid, err := w.s.Bundles.Cut(t.Context(), w.project, strings.TrimSpace(revision), map[string]string{"web": digest, "worker": digest}, map[string]string{"migrate": digest}, []byte(legacy), digestOrigin(claim))
	if err != nil {
		t.Fatal(err)
	}
	// A release served before password declarations were withdrawn: current
	// release admission cannot make that state, so it is written.
	// sql-guarantee: no owner operation can serve a release whose declaration admission now refuses.
	if _, err := w.db.Exec(`UPDATE releases SET bundle_id=$1 WHERE project_id=$2`, bid, w.project); err != nil {
		t.Fatal(err)
	}
	op := w.rehearse()
	if endedAs(op, OperationVerified) {
		t.Fatalf("legacy password declaration rehearsed as verified: %s", op.Detail)
	}
	if !strings.Contains(op.Detail, "AppTown sets the password") {
		t.Fatalf("missing contract refusal: %s", op.Detail)
	}
}
