package scm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/gitexec/gittest"
)

func addOversized(t *testing.T, dir string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "oversized.bin"), make([]byte, maxPublishedFile+1), 0600); err != nil {
		t.Fatal(err)
	}
	gittest.RunAsTester(t, dir, "add", "oversized.bin")
	return commitIn(t, dir, "oversized content")
}

// A publication checks every commit it brings to the branch that is not
// already on the branch or on the default branch, including history
// brought from another branch in the repository.
func TestAPublicationChecksHistoryFromOtherBranches(t *testing.T) {
	f := newPublicationFixture(t)
	// A trusted/imported main or another legacy branch may contain content
	// not previously accepted onto this change. The specified baseline is
	// this change's published tip, not every branch in the hub.
	gittest.RunAsTester(t, f.hub, "checkout", "-qb", "imported")
	tip := addOversized(t, f.hub)
	gittest.RunAsTester(t, f.hub, "checkout", "main")
	writer := f.writer("writer")
	gittest.RunAsTester(t, writer, "merge", "--ff-only", "origin/imported")
	before := f.tip()
	_, err := f.publish(writer, Grant{Generation: f.authority().Generation})
	if !errors.Is(err, ErrNotPublished) || f.tip() != before {
		t.Fatalf("unchecked imported history published: error=%v tip=%s imported=%s", err, f.tip(), tip)
	}
}

// Permission to rewind (a run's restart) never skips the checks of a move
// that adds commits.
func TestPermissionToRewindDoesNotSkipTheChecks(t *testing.T) {
	f := newPublicationFixture(t)
	writer := f.writer("writer")
	tip := addOversized(t, writer)
	before := f.tip()
	_, err := f.publisher.Publish(context.Background(), Publication{ProjectID: f.projectID, Repository: f.hub, Branch: "work", From: writer, Rewind: true, Grant: Grant{Generation: f.authority().Generation}})
	if !errors.Is(err, ErrNotPublished) || f.tip() != before {
		t.Fatalf("restart permission bypassed forward publication checks: error=%v tip=%s proposed=%s", err, f.tip(), tip)
	}
}
