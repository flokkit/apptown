package workflowexec

import (
	"encoding/json"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/fix"
	"github.com/flokkit/apptown/internal/gitexec"
)

func (a *commands) fixProposal(task *workflow.Execution) (bool, string, error) {
	run := task.Run()
	source, err := workflow.ReadSourceIn(task.Context(), a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		return false, "", err
	}
	sel, err := verification.SelectCommit(task.Context(), a.db, a.cfg.DataDir, run, source)
	if err != nil {
		return false, "", err
	}
	base, err := scm.ForkBase(task.Context(), sel.Repo, sel.Repo, source.DefaultBranch, sel.Commit, source.ChangeBaseSHA)
	if err != nil {
		return false, "", err
	}
	ds, err := a.declarations(task.Context(), run)
	if err != nil {
		return false, "", err
	}
	var sides [2][]verification.UpdateListing
	for i, commit := range []string{base, sel.Commit} {
		tree, remove, err := verification.StageCommittedTree(task.Context(), a.cfg.DataDir, sel.Repo, commit)
		if err != nil {
			return false, "", err
		}
		sides[i], err = verification.ListUpdates(task.Context(), a.db, verification.Executor{Run: a.checks.Run, AgentImage: a.checks.Image}, tree, ds)
		remove()
		if err != nil {
			return false, "", err
		}
	}
	moved := []fix.Move{}
	if len(sides[0]) != len(sides[1]) {
		return false, "", fmt.Errorf("the update changed the declared parts")
	}
	for i, before := range sides[0] {
		versions := map[string]string{}
		for _, p := range before.Packages {
			versions[p.Ecosystem+"\x00"+p.Package] = p.Version
		}
		for _, p := range sides[1][i].Packages {
			old := versions[p.Ecosystem+"\x00"+p.Package]
			if old != p.Version {
				moved = append(moved, fix.Move{Ecosystem: p.Ecosystem, Package: p.Package, From: old, To: p.Version})
			}
		}
	}
	listing, err := json.Marshal(sides[1])
	if err != nil {
		return false, "", err
	}
	main, err := gitexec.HubReadRef(task.Context(), sel.Repo, "refs/heads/"+source.DefaultBranch)
	if err != nil {
		return false, "", err
	}
	return task.KeepProposal(main, listing, moved)
}
