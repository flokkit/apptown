package scm

import (
	"context"
	"errors"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/gitlab"
)

func TestSCMGitCancellationStopsAStartedProcess(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	driver := &gitDriver{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := driver.git(ctx, repo, nil, "-c", "alias.wait=!touch ready; sleep 60", "wait")
		done <- err
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	waiting := true
	for waiting {
		select {
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(repo, "ready")); err == nil {
				waiting = false
			}
		case err := <-done:
			t.Fatalf("process stopped before cancellation: %v", err)
		case <-timer.C:
			t.Fatal("Git process did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel result: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled Git process kept running")
	}
}
func TestSCMProviderCredentialsAreFrozenForOneOperation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	calls := 0
	identity := RepositoryIdentity{AttachmentID: idstest.N(2), ProjectID: idstest.N(1), ProviderProjectID: 71, CloneURL: "https://example.invalid/team/p.git"}
	source := Source{ProjectID: identity.ProjectID, MR: MergeRequest{Identity: identity, IID: 1}}
	driver := &gitDriver{dataDir: dir, provider: func(context.Context, RepositoryIdentity) (VerifiedRepository, error) {
		calls++
		client, err := gitlab.New("https://example.invalid", "first")
		return VerifiedRepository{Identity: identity, Provider: ProviderSession{Client: client}, Observed: gitlab.ProjectRef{HTTPURL: identity.CloneURL}}, err
	}}
	first, env, err := driver.providerClient(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	driver.provider = func(context.Context, RepositoryIdentity) (VerifiedRepository, error) {
		t.Fatal("credentials reread during one operation")
		return VerifiedRepository{}, nil
	}
	second, next, err := driver.providerClient(context.Background(), source)
	if err != nil || first != second || !slices.Equal(env, next) || calls != 1 {
		t.Fatalf("credentials were not stable: %v calls=%d", err, calls)
	}
	legacy := source
	legacy.MR.Identity = RepositoryIdentity{}
	if _, _, err := driver.providerClient(context.Background(), legacy); err == nil {
		t.Fatal("legacy MR inherited current provider identity")
	}

}

// A landing's merge is found by its exact Operation trailer.
//
// Kills: mergeCommit matching a trailer that only starts with the
// operation, or one not at the start of a line.
func TestMergeOperationProofRequiresExactTrailer(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	gittest.RunAsTester(t, repo, "init", "-b", "main")
	for _, message := range []string{"AppTown-Operation: land-a.b-extra", "AppTown-Operation: land-axb", "prefix AppTown-Operation: land-a.b"} {
		gittest.RunAsTester(t, repo, "commit", "--allow-empty", "-qm", message)
	}
	if sha, err := (&gitDriver{}).mergeCommit(context.Background(), repo, "main", "land-a.b"); err != nil || sha != "" {
		t.Fatalf("unrelated commit certified operation: %s %v", sha, err)
	}
	gittest.RunAsTester(t, repo, "commit", "--allow-empty", "-qm", "merge\n\nAppTown-Operation: land-c.d")
	current := gittest.RunAsTester(t, repo, "rev-parse", "HEAD")
	if sha, err := (&gitDriver{}).mergeCommit(context.Background(), repo, "main", "land-c.d"); err != nil || sha != current {
		t.Fatalf("exact operation proof under the current key: %s %v", sha, err)
	}
	if _, err := (&gitDriver{}).mergeCommit(context.Background(), repo, "missing", "land-a.b"); err == nil {
		t.Fatal("unreadable history treated as absence")
	}
}

// A landing whose branch is already on main (an empty change, or one merged
// outside AppTown) records main's head as its result only when that head
// holds the tree its checks judged: an empty adoption checked at A, with main
// moved to B before the merge, is refused as "main moved", not landed on B.
// Main at the candidate, or past it by commits that leave its tree as it was, still
// settles as an existing merge (review EA-02, soak 17).
func TestAnAlreadyMergedBranchLandsOnlyTheCheckedTree(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		after func(t *testing.T, repo string)
		moved bool
	}{
		{"main at the candidate", func(*testing.T, string) {}, false},
		{"main moved by a commit that changes no file", func(t *testing.T, repo string) {
			gittest.RunAsTester(t, repo, "commit", "-q", "--allow-empty", "-m", "a commit that changes no file")
		}, false},
		{"main moved after the checks", func(t *testing.T, repo string) {
			if err := os.WriteFile(filepath.Join(repo, "state.txt"), []byte("no longer conforms\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gittest.RunAsTester(t, repo, "commit", "-qam", "main moved after checks")
		}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			gittest.RunAsTester(t, repo, "init", "-q", "-b", "main")
			if err := os.WriteFile(filepath.Join(repo, "state.txt"), []byte("conforming\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gittest.RunAsTester(t, repo, "add", "state.txt")
			gittest.RunAsTester(t, repo, "commit", "-qm", "checked tree")
			checked := gittest.RunAsTester(t, repo, "rev-parse", "HEAD")
			gittest.RunAsTester(t, repo, "branch", "adoption")
			c.after(t, repo)
			head := gittest.RunAsTester(t, repo, "rev-parse", "HEAD")
			source := Source{Repository: repo, Branch: "adoption", DefaultBranch: "main", Candidate: checked, Target: checked, PinRequired: true}
			plan, err := (&gitDriver{}).Prepare(t.Context(), source, "")
			var moved *LandingMoved
			switch {
			case c.moved && (!errors.As(err, &moved) || moved.What != "main" || moved.Found != head):
				t.Fatalf("unchecked main %s: plan %+v, err %v", head, plan, err)
			case !c.moved && (err != nil || plan.Kind != Existing || plan.Result != head):
				t.Fatalf("checked tree at %s: plan %+v, err %v", head, plan, err)
			}
		})
	}
}
