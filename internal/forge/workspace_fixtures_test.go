package forge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/gitexec/gittest"

	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
)

func ensureWorkspaceCloneForTest(a *App, repo, path, branch string) error {
	_, err := (workflow.GitWorkspace{DataDir: a.cfg.dataDir}).EnsureClone(context.Background(), workflow.CloneRequest{Repository: repo, Workspace: path, Branch: branch})
	return err
}
func prepareChangeWorkspaceForTest(a *App, p *project.Project, ch *workflow.Change) error {
	return a.Work.PrepareWorkspace(a.asForge(context.Background(), p.ID, "test"), ch.ID)
}

// mirrorBranchForTest puts a workspace's branch into the hub as fixture
// setup. It is plain git, not the product path: production publishes only
// through scm.Branches, which checks who holds the branch.
func mirrorBranchForTest(repo, ws, branch string) error {
	out, err := gitexec.Run(context.Background(), repo, "fetch", "--quiet", ws, "+refs/heads/"+branch+":refs/heads/"+branch)
	if err != nil {
		return fmt.Errorf("mirroring %s: %w: %s", branch, err, out)
	}
	return nil
}

// stageAll panics on a failed staging (it has no test to fail): a fixture
// that staged nothing would otherwise commit nothing silently.
func stageAll(dir string) {
	if err := scm.StageAll(context.Background(), dir); err != nil {
		panic(err)
	}
}

// publishChangeWorkForTest commits a file in the change's folder and
// publishes it under the branch's current grant, as the change's worker does
// while a landing runs.
func publishChangeWorkForTest(t *testing.T, a *App, source workflow.MergeSource, name, body, message string) {
	t.Helper()
	folder := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", source.ChangeID))
	if err := os.WriteFile(filepath.Join(folder, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, folder, "add", name)
	gittest.Run(t, folder, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", message)
	authority, err := scm.ReadAuthorityIn(context.Background(), a.DB, source.ProjectID, source.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.branches.Publish(context.Background(), scm.Publication{ProjectID: source.ProjectID, Repository: source.Repository(a.cfg.dataDir),
		Branch: source.Branch, From: folder, Grant: scm.Grant{Generation: authority.Generation}}); err != nil {
		t.Fatal(err)
	}
}
