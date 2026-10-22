package scm

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/gitlab"
	"github.com/flokkit/apptown/internal/gitlab/gitlabtest"
)

// A landing evaluates its merge both on entry to ship and at completion.
// An unchanged merged request must return its recorded result on both calls.
func TestARecordedProviderMergeCompletesItsLanding(t *testing.T) {
	t.Parallel()
	service, claim := serviceFixture(t)
	repo := t.TempDir()
	gittest.RunAsTester(t, repo, "init", "-b", "main")
	gittest.RunAsTester(t, repo, "commit", "--allow-empty", "-qm", "base")
	base := gittest.RunAsTester(t, repo, "rev-parse", "HEAD")
	gittest.RunAsTester(t, repo, "checkout", "-qb", "change")
	gittest.RunAsTester(t, repo, "commit", "--allow-empty", "-qm", "candidate")
	candidate := gittest.RunAsTester(t, repo, "rev-parse", "HEAD")
	gittest.RunAsTester(t, repo, "checkout", "main")
	gittest.RunAsTester(t, repo, "merge", "--ff-only", "change")
	remote := filepath.Join(t.TempDir(), "remote.git")
	gittest.RunAsTester(t, t.TempDir(), "clone", "--bare", repo, remote)
	gittest.RunAsTester(t, repo, "remote", "add", "origin", remote)
	claim.source.Repository = repo
	claim.source.Branch = "change"
	claim.source.Candidate = candidate
	claim.source.Target = base
	claim.source.MR = MergeRequest{Project: "p", IID: 1}
	fake := gitlabtest.New(t)
	fake.Put("p", gitlab.MergeRequest{IID: 1, State: "merged", SHA: candidate, MergeCommitSHA: candidate, SourceBranch: "change", TargetBranch: "main"})
	drivers := GitDrivers{DataDir: t.TempDir(), Provider: func(context.Context) (*gitlab.Client, error) { return fake.Client() }}
	ctx := authorized(NewEffect)
	for _, phase := range []string{"enter ship", "complete landing"} {
		result, err := service.Merge(ctx, claim.source.RunID, drivers.Driver())
		if err != nil || !result.Merged || result.SHA != candidate {
			t.Fatalf("%s: result=%+v error=%v; want recorded merge %s", phase, result, err, candidate)
		}
	}
}
