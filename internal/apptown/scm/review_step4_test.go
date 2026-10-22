package scm

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

// Branch publication's recovery read is part of the hardened hub path.
// An ambient GIT_DIR must not make it inspect another repository.
func TestReviewS4PublicationRecoveryIgnoresGitDir(t *testing.T) {
	f := newPublicationFixture(t)
	writer := f.writer("writer")
	tip := commitIn(t, writer, "result")
	t.Cleanup(f.db.FailNextStatement("-- name: SettlePublication"))
	if _, err := f.publish(writer, Grant{Generation: f.authority().Generation}); err == nil {
		t.Fatal("settlement fault was not exercised")
	}
	if f.tip() != tip {
		t.Fatal("publication did not move the branch")
	}
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	gittest.RunAsTester(t, t.TempDir(), "init", "-q", "-b", "main", elsewhere)
	commitIn(t, elsewhere, "unrelated")
	t.Setenv("GIT_DIR", filepath.Join(elsewhere, ".git"))
	got, err := f.publisher.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Settled != 1 || got.Unknown != 0 {
		t.Fatalf("hardened recovery read ambient GIT_DIR: %+v", got)
	}
}
