package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"

	"flokkit.io/forge/internal/forge/dataprotection"

	"flokkit.io/forge/internal/forge/project"
)

type Backups struct {
	projects *project.Projects
	data     *dataprotection.Service
	dataDir  string
}

func NewBackups(projects *project.Projects, data *dataprotection.Service, dataDir string) Backups {
	return Backups{projects: projects, data: data, dataDir: dataDir}
}

// The backup routes' responses.
type (
	// DeploymentBackups is one deployment's backup sets, what a backup of it
	// would take, and how many sets are kept.
	DeploymentBackups struct {
		Backups []dataprotection.Backup         `json:"backups"`
		Plan    []dataprotection.BackupPlanItem `json:"plan"`
		Note    string                          `json:"note"`
		Keep    int                             `json:"keep"`
	}
	// BackupTaken is the outcome of a backup taken now.
	BackupTaken struct {
		OK     bool                   `json:"ok"`
		Backup *dataprotection.Backup `json:"backup"`
	}
	// AllBackups is every backup set the forge holds and where they live.
	AllBackups struct {
		Backups []dataprotection.ProjectBackup `json:"backups"`
		Keep    int                            `json:"keep"`
		Where   string                         `json:"where"`
	}
)

// ListBackups: GET /projects/{slug}/deployments/{name}/backups
func (h Backups) ListBackups(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	bs, err := h.data.DeploymentSets(r.Context(), p.ID, r.PathValue("name"), 50)
	if err != nil {
		WriteFault(w, err)
		return
	}
	plan, why := h.data.BackupPlan(r.Context(), p.ID, p.Slug, r.PathValue("name"))
	WriteJSON(w, 200, DeploymentBackups{Backups: bs, Plan: plan, Note: why, Keep: dataprotection.BackupKeep})
}

// TakeBackup: POST /projects/{slug}/deployments/{name}/backups — now.
func (h Backups) TakeBackup(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	// A copy, once begun, finishes though the request ends.
	b, err := h.data.TakeBackup(context.WithoutCancel(r.Context()), p.ID, r.PathValue("name"), "manual", Actor(r))
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
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	id, err := PathID(r, "id")
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
		WriteAdmitted(w, op)
	})
}

// AllBackups: GET /backups — every set the forge holds, newest first,
// with the project's name; the Backups tab reads it.
func (h Backups) AllBackups(w http.ResponseWriter, r *http.Request) {
	out, err := h.data.AllSets(r.Context(), 200)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, AllBackups{Backups: out, Keep: dataprotection.BackupKeep, Where: filepath.Join(h.dataDir, "backups")})
}
