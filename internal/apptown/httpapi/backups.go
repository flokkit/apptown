package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/project"
)

type Backups struct {
	projects *project.Projects
	data     *dataprotection.Service
	releases *deployment.Releases
}

func NewBackups(projects *project.Projects, data *dataprotection.Service, releases *deployment.Releases) Backups {
	return Backups{projects: projects, data: data, releases: releases}
}

// The backup routes' responses.
type (
	// DeploymentBackups is one deployment's backup sets, what a backup of it
	// would take, and how many sets are kept.
	DeploymentBackups struct {
		Backups []dataprotection.ProjectBackup  `json:"backups"`
		Plan    []dataprotection.BackupPlanItem `json:"plan"`
		Note    string                          `json:"note"`
		Keep    int                             `json:"keep"`
	}
	// BackupTaken is the outcome of a backup taken now.
	BackupTaken struct {
		OK     bool                   `json:"ok"`
		Backup *dataprotection.Backup `json:"backup"`
	}
	// AllBackups is every backup set AppTown holds and where they live.
	AllBackups struct {
		Backups   []dataprotection.ProjectBackup `json:"backups"`
		Keep      int                            `json:"keep"`
		Where     string                         `json:"where"`
		Retention string                         `json:"retention"`
	}
)

// ListBackups: GET /projects/{slug}/deployments/{name}/backups
func (h Backups) ListBackups(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	bs, err := h.data.DeploymentSets(r.Context(), p.ID, r.PathValue("name"), 50)
	if err != nil {
		WriteFault(w, err)
		return
	}
	out := make([]dataprotection.ProjectBackup, 0, len(bs))
	for _, b := range bs {
		out = append(out, dataprotection.ProjectBackup{Backup: b, Project: p.Slug, DeploymentLabel: b.Deployment})
	}
	if err := h.beforeReleases(r.Context(), out); err != nil {
		WriteFault(w, err)
		return
	}
	plan, why := h.data.BackupPlan(r.Context(), p.ID, p.Slug, r.PathValue("name"))
	WriteJSON(w, 200, DeploymentBackups{Backups: out, Plan: plan, Note: why, Keep: dataprotection.BackupKeep})
}

// beforeReleases names the release each pre-release set was taken before.
func (h Backups) beforeReleases(ctx context.Context, out []dataprotection.ProjectBackup) error {
	byProject := map[ids.ID][]ids.ID{}
	for _, b := range out {
		if b.Trigger == dataprotection.TriggerPreRelease {
			byProject[b.ProjectID] = append(byProject[b.ProjectID], b.ID)
		}
	}
	for projectID, ids := range byProject {
		numbers, err := h.releases.BeforeBackups(ctx, projectID, ids)
		if err != nil {
			return err
		}
		for i := range out {
			if out[i].ProjectID == projectID {
				out[i].BeforeRelease = numbers[out[i].ID]
			}
		}
	}
	return nil
}

// TakeBackup: POST /projects/{slug}/deployments/{name}/backups — now.
func (h Backups) TakeBackup(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	// A copy, once begun, finishes though the request ends.
	b, err := h.data.TakeBackup(context.WithoutCancel(r.Context()), p.ID, r.PathValue("name"), actor(r))
	if err != nil {
		WriteError(w, 422, "%v", err)
		return
	}
	writeBackupTaken(w, b)
}

// writeBackupTaken answers a backup taken: the record, with 502 when the
// backup failed — the failure is recorded, and the record is the answer
// (described in the API description as the operation's 502).
func writeBackupTaken(w http.ResponseWriter, b *dataprotection.Backup) {
	code := 200
	if b.Status == dataprotection.BackupFailed {
		code = 502
	}
	WriteJSON(w, code, BackupTaken{OK: b.Status != dataprotection.BackupFailed, Backup: b})
}

// RestoreBackup: POST /projects/{slug}/deployments/{name}/backups/{id}/restore
// {confirm} — confirm must equal the deployment's name. The restore runs
// as an operation: the answer is the operation admitted (202), which
// apptown wait operation and its page follow; its result's restore is the
// receipt.
func (h Backups) RestoreBackup(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Confirm string `json:"confirm"`
	}) {
		if strings.TrimSpace(in.Confirm) != r.PathValue("name") {
			WriteError(w, 400, "confirm must equal the deployment name %q — a restore replaces what is there", r.PathValue("name"))
			return
		}
		op, err := h.data.RequestRestore(r.Context(), p.ID, r.PathValue("name"), id)
		if err != nil {
			WriteFaultOr(w, err, 422)
			return
		}
		writeAdmitted(w, op)
	})
}

// AllBackups: GET /backups — every set AppTown holds, newest first,
// with the project's name; the Backups tab reads it.
func (h Backups) AllBackups(w http.ResponseWriter, r *http.Request) {
	out, err := h.data.AllSets(r.Context(), 200)
	if err != nil {
		WriteFault(w, err)
		return
	}
	if err := h.beforeReleases(r.Context(), out); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, AllBackups{Backups: out, Keep: dataprotection.BackupKeep, Retention: dataprotection.BackupRetention(), Where: h.data.BackupDirectory()})
}

// BackupDeployments: GET /backups/deployments — every deployment that
// holds data, or may, with its protection as its own read answers it, the
// newest set's time and size, and the nightly count. The Backups page reads it.
func (h Backups) BackupDeployments(w http.ResponseWriter, r *http.Request) {
	out, err := h.data.FleetProtection(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, out)
}
