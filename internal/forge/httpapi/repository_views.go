package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/repositoryedit"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
)

type RepositoryViews struct {
	projects  *project.Projects
	access    *identity.Service
	db        *database.DB
	dataDir   string
	integrity *repositoryedit.Integrity
	releases  *deployment.Releases
}

func NewRepositoryViews(projects *project.Projects, access *identity.Service, db *database.DB, dataDir string, integrity *repositoryedit.Integrity, releases *deployment.Releases) RepositoryViews {
	return RepositoryViews{projects: projects, access: access, db: db, dataDir: dataDir, integrity: integrity, releases: releases}
}

// RepoIntegrity: POST /api/v1/projects/{slug}/repo/integrity
// {action: "adopt"|"discard"} — the ONLY sanctioned exits from drift.
// adopt commits the edits with the deciding admin on record; discard
// restores the clean tree. Either way the audit log keeps what was found.
func (h RepositoryViews) RepoIntegrity(w http.ResponseWriter, r *http.Request) {
	if !RequireAdmin(h.access, w, r) {
		return
	}
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	repo := filepath.Join(h.dataDir, "workspaces", "project-"+p.Slug)
	if !repositoryExists(repo) {
		WriteError(w, 422, "project is not repo-backed")
		return
	}
	jsonCommand(w, r, jsonBody{errorText: `action must be "adopt" (keep the edits) or "discard" (throw them away)`}, func(in struct {
		Action string `json:"action"`
	}) {
		if in.Action != "adopt" && in.Action != "discard" {
			WriteError(w, 400, `action must be "adopt" (keep the edits) or "discard" (throw them away)`)
			return
		}

		result, err := h.integrity.Resolve(r.Context(), p, in.Action, Actor(r))
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, result)
	})
}

// RunDiff serves the run branch's diff against the default branch —
// the "compare" materialization rung.
func (h RepositoryViews) RunDiff(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	run, err := workflow.ReadRunIn(r.Context(), h.db, id)
	if err != nil {
		writeReadFault(w, err, "no such run")
		return
	}
	source, err := workflow.ReadSourceIn(r.Context(), h.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		WriteFault(w, err)
		return
	}
	if !source.RepoBacked() {
		WriteError(w, 422, "project is not repo-backed")
		return
	}
	repo, branch := source.Repository(h.dataDir), source.Branch
	repo, err = scm.ComparisonRepository(r.Context(), repo, source.Workspace(h.dataDir))
	if err != nil {
		WriteFault(w, err)
		return
	}
	if _, err := gitexec.Run(r.Context(), repo, "rev-parse", "--verify", branch); err != nil {
		WriteError(w, 404, "run has no branch")
		return
	}
	db := source.DefaultBranch
	// Prefer the recorded fork point (survives merge); fall back to
	// three-dot for pre-base_sha runs, then to run-tagged commit discovery
	// for historical MERGED runs whose three-dot diff is empty.
	baseSHA := source.RootBaseSHA
	var diff string
	if baseSHA != "" {
		var comparison scm.CommittedDiff
		comparison, err = scm.ReadCommittedDiff(r.Context(), repo, baseSHA, branch)
		if err == nil {
			diff, err = comparison.Patch(r.Context(), repo)
		}
	} else {
		diff, err = gitexec.Run(r.Context(), repo, "diff", db+"..."+branch)
		if err == nil && strings.TrimSpace(diff) == "" {
			// Historical merged run: bound its commits by message tag.
			tag := fmt.Sprintf("run %d)", source.RootRunID)
			var out string
			out, err = gitexec.Run(r.Context(), repo, "log", "--format=%H %s", branch)
			var shas []string
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(line, tag) {
					shas = append(shas, strings.Fields(line)[0])
				}
			}
			if len(shas) > 0 {
				first, last := shas[len(shas)-1], shas[0]
				diff, err = gitexec.Run(r.Context(), repo, "diff", first+"^.."+last)
			}
		}
	}
	if err != nil {
		WriteFault(w, err)
		return
	}
	if len(diff) > 512*1024 {
		diff = diff[:512*1024] + "\n... (truncated)"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(diff))
}

// ChangeDiff: GET /api/v1/changes/{id}/diff — the Change's whole diff, from
// its base (scm.ChangeBase: it survives catching up with main and landing).
func (h RepositoryViews) ChangeDiff(w http.ResponseWriter, r *http.Request) {
	ch, p, ok := ChangeAndProject(h.db, w, r)
	if !ok {
		return
	}
	// A change with no code of its own has no branch to compare: its diff
	// is what its release put live that the release before it did not.
	noCode, err := workflow.CarriesNoCodeIn(r.Context(), h.db, ch)
	if err != nil {
		WriteFault(w, err)
		return
	}
	hub := filepath.Join(h.dataDir, "workspaces", "project-"+p.Slug)
	if noCode && !branchPublished(r.Context(), hub, ch.Branch) {
		cmp, comparison, read := shippedComparison(r.Context(), h.releases, hub, ch)
		if !read {
			WriteError(w, 404, "%s", cmp.Message)
			return
		}
		h.writeDiff(w, r, hub, comparison)
		return
	}
	if ch.Branch == "" {
		WriteError(w, 404, "change has no branch")
		return
	}
	repo, err := changeRepository(r.Context(), h.dataDir, p.Slug, ch.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	if _, err := gitexec.Run(r.Context(), repo, "rev-parse", "--verify", ch.Branch); err != nil {
		WriteError(w, 404, "branch %s is gone", ch.Branch)
		return
	}
	comparison, err := scm.ReadChangeDiff(r.Context(), hub, repo, p.DefaultBranch, ch.BaseSHA, ch.Branch)
	if err != nil {
		WriteFault(w, err)
		return
	}
	h.writeDiff(w, r, repo, comparison)
}

// writeDiff writes a comparison's patch, cut at 512 KiB.
func (h RepositoryViews) writeDiff(w http.ResponseWriter, r *http.Request, repo string, comparison scm.CommittedDiff) {
	diff, err := comparison.Patch(r.Context(), repo)
	if err != nil {
		WriteFault(w, err)
		return
	}
	if len(diff) > 512*1024 {
		diff = diff[:512*1024] + "\n... (truncated)"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(diff))
}

// changeRepository is where a change's committed work is read: its folder
// when that holds the published branch, else the project's repository.
func changeRepository(ctx context.Context, dataDir, slug string, changeID ids.ID) (string, error) {
	return scm.ComparisonRepository(ctx, filepath.Join(dataDir, "workspaces", "project-"+slug), filepath.Join(dataDir, "workspaces", fmt.Sprintf("change-%d", changeID)))
}

func repositoryExists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}
