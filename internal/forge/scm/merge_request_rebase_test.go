package scm

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/gitlab"
	"flokkit.io/forge/internal/gitlab/gitlabtest"
)

// Both ff and rebase_merge can automatically rebase at merge time. The
// actual Project API field must be checked before dispatching a merge.
func TestAMergeRequestIsRefusedWhenGitLabRebasesAutomatically(t *testing.T) {
	for _, method := range []string{"ff", "rebase_merge"} {
		t.Run(method, func(t *testing.T) {
			repo := t.TempDir()
			gittest.RunAsTester(t, repo, "init", "-b", "main")
			gittest.RunAsTester(t, repo, "commit", "--allow-empty", "-qm", "base")
			base := gittest.RunAsTester(t, repo, "rev-parse", "HEAD")
			gittest.RunAsTester(t, repo, "checkout", "-qb", "change")
			gittest.RunAsTester(t, repo, "commit", "--allow-empty", "-qm", "candidate")
			candidate := gittest.RunAsTester(t, repo, "rev-parse", "HEAD")
			gittest.RunAsTester(t, repo, "checkout", "main")
			remote := filepath.Join(t.TempDir(), "remote.git")
			gittest.RunAsTester(t, t.TempDir(), "clone", "--bare", repo, remote)
			gittest.RunAsTester(t, repo, "remote", "add", "origin", remote)
			fake := gitlabtest.New(t)
			yes := true
			fake.Settings(gitlab.MergeSettings{MergeMethod: method, SquashOption: "never", AutomaticRebase: &yes})
			fake.Put("p", gitlab.MergeRequest{IID: 1, State: "opened", SHA: candidate, SourceBranch: "change", TargetBranch: "main"})
			fake.OnBranch = func(string, string) (string, error) { return base, nil }
			driver := &gitDriver{dataDir: t.TempDir(), provider: func(context.Context) (*gitlab.Client, error) { return fake.Client() }}
			plan, err := driver.Prepare(context.Background(), Source{Repository: repo, Branch: "change", DefaultBranch: "main", Candidate: candidate, Target: base, PinRequired: true, MR: MergeRequest{Project: "p", IID: 1}}, "")
			if !errors.Is(err, ErrMergeRequestSettings) {
				t.Fatalf("automatic rebasing not refused: plan=%+v err=%v", plan, err)
			}
		})
	}
}

// A project whose settings could merge something other than the candidate
// (plain merge commits, squashing, or rebasing on the provider, including
// when GitLab does not say whether it rebases) is refused, naming the
// setting to change. Fast-forward and semi-linear merges without either
// land exactly the candidate.
func TestAMergeRequestIsRefusedUnderSettingsThatChangeWhatLands(t *testing.T) {
	t.Parallel()
	yes, no := true, false
	for name, c := range map[string]struct {
		settings gitlab.MergeSettings
		squash   bool
		says     string
	}{
		"fast-forward":        {settings: gitlab.MergeSettings{MergeMethod: "ff", SquashOption: "default_off", AutomaticRebase: &no}},
		"semi-linear":         {settings: gitlab.MergeSettings{MergeMethod: "rebase_merge", SquashOption: "never", AutomaticRebase: &no}},
		"merge commits":       {settings: gitlab.MergeSettings{MergeMethod: "merge", SquashOption: "default_off", AutomaticRebase: &no}, says: "merge method"},
		"project squash":      {settings: gitlab.MergeSettings{MergeMethod: "ff", SquashOption: "always", AutomaticRebase: &no}, says: "squash"},
		"request squash":      {settings: gitlab.MergeSettings{MergeMethod: "ff", SquashOption: "default_off", AutomaticRebase: &no}, squash: true, says: "squash"},
		"automatic rebase":    {settings: gitlab.MergeSettings{MergeMethod: "ff", SquashOption: "default_off", AutomaticRebase: &yes}, says: "rebases automatically"},
		"rebase not reported": {settings: gitlab.MergeSettings{MergeMethod: "ff", SquashOption: "default_off"}, says: "does not say"},
	} {
		err := checkedMergeSettings(c.settings, &gitlab.MergeRequest{Squash: c.squash})
		if c.says == "" && err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
		if c.says != "" && (!errors.Is(err, ErrMergeRequestSettings) || !strings.Contains(err.Error(), c.says)) {
			t.Errorf("%s: %v, want a refusal naming %q", name, err, c.says)
		}
	}
}
