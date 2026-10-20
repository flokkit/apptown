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
	gittest.Run(t, hub, "branch", "apptown/change-00000000-0007-7000-8000-000000000000")
	gittest.Run(t, hub, "branch", "apptown/run-00000000-0009-7000-8000-000000000000")
	gittest.RunAsTester(t, hub, "commit", "-q", "--allow-empty", "-m", "not landed")
	open := gittest.Run(t, hub, "rev-parse", "HEAD")
	gittest.Run(t, hub, "branch", "apptown/change-00000000-0008-7000-8000-000000000000")
	gittest.Run(t, hub, "update-ref", "refs/apptown/refused/chat-abc", open)
	gittest.Run(t, hub, "update-ref", "refs/apptown/candidates/step-3-a1", open)
	gittest.Run(t, hub, "update-ref", "refs/apptown/scratch/1", open)
	gittest.Run(t, hub, "update-ref", "refs/apptown/incoming/ticket-1", open)
	gittest.Run(t, hub, "update-ref", "refs/apptown/published", open)
	gittest.Run(t, hub, "reset", "-q", "--hard", first)

	move, stay, err := exportRefs(ctx, hub, "main", []string{"apptown/change-00000000-0007-7000-8000-000000000000"}, []ids.ID{idstest.N(9)})
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
	if got, want := names(move), []string{"refs/heads/apptown/change-00000000-0007-7000-8000-000000000000", "refs/heads/apptown/run-00000000-0009-7000-8000-000000000000", "refs/heads/main", "refs/tags/v1"}; !slices.Equal(got, want) {
		t.Fatalf("carried %v, want %v", got, want)
	}
	if got, want := names(stay), []string{"refs/apptown/candidates/step-3-a1", "refs/apptown/refused/chat-abc", "refs/heads/apptown/change-00000000-0008-7000-8000-000000000000"}; !slices.Equal(got, want) {
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

// Changes and runs made before the AppTown rename keep their recorded
// branches (forge/change-N, forge/run-N), and an export carries them like
// the new spellings.
func TestAnExportCarriesBranchesRecordedBeforeTheRename(t *testing.T) {
	ctx := context.Background()
	hub := t.TempDir()
	gittest.Run(t, hub, "init", "-q", "-b", "main")
	gittest.RunAsTester(t, hub, "commit", "-q", "--allow-empty", "-m", "first")
	gittest.Run(t, hub, "branch", "forge/change-00000000-0007-7000-8000-000000000000")
	gittest.Run(t, hub, "branch", "forge/run-00000000-0009-7000-8000-000000000000")
	gittest.Run(t, hub, "branch", "forge/change-00000000-0008-7000-8000-000000000000")

	move, stay, err := exportRefs(ctx, hub, "main", []string{"forge/change-00000000-0007-7000-8000-000000000000"}, []ids.ID{idstest.N(9)})
	if err != nil {
		t.Fatal(err)
	}
	var carried, kept []string
	for _, r := range move {
		carried = append(carried, r.Ref)
	}
	for _, r := range stay {
		kept = append(kept, r.Ref+" "+r.Kind)
	}
	slices.Sort(carried)
	if want := []string{"refs/heads/forge/change-00000000-0007-7000-8000-000000000000", "refs/heads/forge/run-00000000-0009-7000-8000-000000000000", "refs/heads/main"}; !slices.Equal(carried, want) {
		t.Fatalf("carried %v, want %v", carried, want)
	}
	if want := []string{"refs/heads/forge/change-00000000-0008-7000-8000-000000000000 change"}; !slices.Equal(kept, want) {
		t.Fatalf("stayed %v, want the open change's branch %v", kept, want)
	}
}
