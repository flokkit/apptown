package projectlifecycle

import (
	"context"
	"slices"
	"testing"

	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// A move carries the default branch, tags and the branches of the changes
// and runs that move; refused and candidate work, and the branches of what
// stays, stay and are listed; transient refs are neither. A commit the
// moving records name that no carried ref reaches is named.
func TestAnExportCarriesLandedRefsAndFindsCommitsTheyDoNotReach(t *testing.T) {
	ctx := context.Background()
	hub := t.TempDir()
	gittest.Run(t, hub, "init", "-q", "-b", "main")
	gittest.RunAsTester(t, hub, "commit", "-q", "--allow-empty", "-m", "first")
	first := gittest.Run(t, hub, "rev-parse", "HEAD")
	gittest.Run(t, hub, "tag", "v1")
	gittest.Run(t, hub, "branch", "forge/change-00000000-0007-7000-8000-000000000000")
	gittest.Run(t, hub, "branch", "forge/run-00000000-0009-7000-8000-000000000000")
	gittest.RunAsTester(t, hub, "commit", "-q", "--allow-empty", "-m", "not landed")
	open := gittest.Run(t, hub, "rev-parse", "HEAD")
	gittest.Run(t, hub, "branch", "forge/change-00000000-0008-7000-8000-000000000000")
	gittest.Run(t, hub, "update-ref", "refs/forge/refused/chat-abc", open)
	gittest.Run(t, hub, "update-ref", "refs/forge/candidates/step-3-a1", open)
	gittest.Run(t, hub, "update-ref", "refs/forge/scratch/1", open)
	gittest.Run(t, hub, "update-ref", "refs/forge/incoming/ticket-1", open)
	gittest.Run(t, hub, "update-ref", "refs/forge/published", open)
	gittest.Run(t, hub, "reset", "-q", "--hard", first)

	move, stay, err := exportRefs(ctx, hub, "main", []string{"forge/change-00000000-0007-7000-8000-000000000000"}, []ids.ID{idstest.N(9)})
	if err != nil {
		t.Fatal(err)
	}
	names := func(refs []ExportRef) []string {
		var out []string
		for _, r := range refs {
			out = append(out, r.Ref)
		}
		slices.Sort(out)
		return out
	}
	if got, want := names(move), []string{"refs/heads/forge/change-00000000-0007-7000-8000-000000000000", "refs/heads/forge/run-00000000-0009-7000-8000-000000000000", "refs/heads/main", "refs/tags/v1"}; !slices.Equal(got, want) {
		t.Fatalf("carried %v, want %v", got, want)
	}
	if got, want := names(stay), []string{"refs/forge/candidates/step-3-a1", "refs/forge/refused/chat-abc", "refs/heads/forge/change-00000000-0008-7000-8000-000000000000"}; !slices.Equal(got, want) {
		t.Fatalf("stayed %v, want %v", got, want)
	}
	missing, err := unreachable(ctx, hub, move, []string{first, open})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(missing, []string{open}) {
		t.Fatalf("unreachable %v, want only the commit that stays %s", missing, open)
	}
}
