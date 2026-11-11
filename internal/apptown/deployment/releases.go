package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/stamp"
)

// A release (design/subsystems/deployment.md §2.8) is the institutional
// record of what shipped, where, as which generation: numbered per project,
// with the changes it carried and the timestamped log it wrote as it
// worked. Creation and settlement have their own owners; Releases is the
// ledger's read side and the startup repair of releases nobody owns.

// Release is one row of the release ledger.
type Release struct {
	RebuiltFrom   int64  `json:"rebuiltFrom,omitempty"`
	RebuiltCommit string `json:"rebuiltCommit,omitempty"`
	// LastStage is historical when recovery cannot confirm current execution.
	LastStage            deploy.DeployStage `json:"lastStage,omitempty"`
	InterruptedByRestart bool               `json:"interruptedByRestart,omitempty"`
	ProgressUncertain    bool               `json:"progressUncertain,omitempty"`
	Stage                deploy.DeployStage `json:"stage,omitempty"`
	StageEnteredAt       stamp.Time         `json:"stageEnteredAt,omitzero"`
	StageElapsedMs       *int64             `json:"stageElapsedMs,omitempty"`
	FailedStage          deploy.DeployStage `json:"failedStage,omitempty"`
	RollbackOf           ids.ID             `json:"rollbackOf,omitzero"`
	RestoreBackupID      ids.ID             `json:"restoreBackupId,omitzero"`
	ApprovalRole         string             `json:"approvalRole"`
	// ApprovalRoleTitle is the approval role as people read it ("Release
	// manager"); empty when no role is required or the role is not stock.
	ApprovalRoleTitle string           `json:"approvalRoleTitle,omitempty"`
	Approval          *ReleaseApproval `json:"approval,omitempty"`
	SupersededBy      ids.ID           `json:"supersededBy,omitzero"`
	FromDeploymentID  ids.ID           `json:"fromDeploymentId,omitzero"`
	BackupID          ids.ID           `json:"backupId,omitzero"`
	ID                ids.ID           `json:"id"`
	ProjectID         ids.ID           `json:"projectId"`
	Number            int64            `json:"number"`
	State             ReleaseState     `json:"state"`
	Deployment        string           `json:"deployment"`
	ExternalRef       string           `json:"externalRef,omitempty"`
	ApprovedBy        string           `json:"approvedBy,omitempty"`
	RunID             ids.ID           `json:"runId,omitzero"`
	CreatedAt         string           `json:"createdAt"`
	ReleasedAt        stamp.Time       `json:"releasedAt,omitzero"`
	Notes             string           `json:"notes,omitempty"`
	// Failure is the recorded failure event, separate from release notes.
	Failure   string `json:"failure,omitempty"`
	CommitSHA string `json:"commitSha,omitempty"`
	SourceSHA string `json:"-"` // the tree a released image was built from
	// §2.8: what shipped, where, as which generation.
	BundleID     ids.ID `json:"bundleId,omitzero"`
	DeploymentID ids.ID `json:"deploymentId,omitzero"`
	Generation   int64  `json:"generation,omitempty"`
	ArtifactHash string `json:"artifactHash,omitempty"`
	// Origin is shipped, or observed-after-restore: found running on the
	// provider after AppTown was restored, which AppTown did not ship
	// (restore_check.go).
	Origin string `json:"origin"`
}

// At is when the release happened: when it was released, else when it was cut.
func (r *Release) At() string {
	if r.ReleasedAt.Valid {
		return stamp.Of(r.ReleasedAt.Time).String()
	}
	return r.CreatedAt
}

// ReleaseEvent is one line of a release's log.
type ReleaseEvent struct {
	At   string `json:"at"`
	Text string `json:"text"`
}

type Releases struct {
	db    *database.DB
	clock func() time.Time // AppTown's clock, which stamps a release; nil is the real one
}

func NewReleases(db *database.DB) *Releases { return &Releases{db: db} }

func (s *Releases) now() time.Time { return clock.Or(s.clock)() }

// roleTitle is a stock role's title, for pages that name who must approve.
func roleTitle(name string) string {
	if r := access.RoleByName(name); r != nil {
		return r.Title
	}
	return ""
}

func (s *Releases) record(r deploymentsql.Release, err error) (*Release, error) {
	if err != nil {
		return nil, err
	}
	v := &Release{RebuiltFrom: r.RebuiltFrom, RebuiltCommit: r.RebuiltCommit,
		StageEnteredAt: stamp.Time{NullTime: r.StageEnteredAt},
		RollbackOf:     r.RollbackOf, RestoreBackupID: r.RestoreBackupID,
		ApprovalRole: r.ApprovalRole, ApprovalRoleTitle: roleTitle(r.ApprovalRole), SupersededBy: r.SupersededBy,
		BackupID:         r.BackupID,
		FromDeploymentID: r.FromDeploymentID,
		ID:               r.ID,
		ProjectID:        r.ProjectID,
		Number:           r.Number,
		Deployment:       r.Deployment,
		ExternalRef:      r.ExternalRef,
		ApprovedBy:       r.ApprovedBy,
		RunID:            r.RunID,
		CreatedAt:        r.CreatedAt.UTC().Format(time.RFC3339Nano),
		ReleasedAt:       stamp.Time{NullTime: r.ReleasedAt},
		Notes:            r.Notes,
		CommitSHA:        r.CommitSha,
		SourceSHA:        r.SourceSha,
		BundleID:         r.BundleID,
		DeploymentID:     r.DeploymentID,
		Generation:       r.Generation,
		ArtifactHash:     r.ArtifactHash,
		Origin:           r.Origin,
	}
	if r.Stage != "" {
		v.Stage, err = deploy.ParseDeployStage(r.Stage)
		if err != nil {
			return nil, err
		}
	}
	if r.FailedStage != "" {
		v.FailedStage, err = deploy.ParseDeployStage(r.FailedStage)
		if err != nil {
			return nil, err
		}
	}
	if r.StageEnteredAt.Valid {
		end := s.now()
		if r.StageFinishedAt.Valid {
			end = r.StageFinishedAt.Time
		}
		elapsed := max(int64(0), end.Sub(r.StageEnteredAt.Time).Milliseconds())
		v.StageElapsedMs = &elapsed
	}
	if r.ApprovalJson != nil {
		v.Approval = &ReleaseApproval{}
		if err := json.Unmarshal(*r.ApprovalJson, v.Approval); err != nil {
			return nil, err
		}
	}
	if err := v.State.Scan(r.State); err != nil {
		return nil, err
	}
	return v, nil
}

// read decorates a ledger row with recovery facts from the recorded attempt.
func (s *Releases) read(ctx context.Context, r deploymentsql.Release, err error) (*Release, error) {
	v, err := s.record(r, err)
	if err != nil {
		return nil, err
	}
	if v.State == Releasing {
		progress, err := deploymentsql.New(s.db.SQL()).ReleaseRecoveryProgress(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		v.ProgressUncertain, v.InterruptedByRestart = progress.Uncertain, progress.InterruptedByRestart
		if v.ProgressUncertain {
			v.LastStage, v.Stage = v.Stage, 0
			v.StageElapsedMs = nil
		}
	}

	return v, nil
}

func (s *Releases) records(ctx context.Context, rows []deploymentsql.Release, err error) ([]*Release, error) {
	if err != nil {
		return nil, err
	}
	var out []*Release
	for _, r := range rows {
		v, err := s.read(ctx, r, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// ByNumber is a project's release by its number.
func (s *Releases) ByNumber(ctx context.Context, projectID ids.ID, number int64) (*Release, error) {
	return s.byNumber(ctx, projectID, number)
}

func (s *Releases) byNumber(ctx context.Context, projectID ids.ID, number int64) (*Release, error) {
	row, err := deploymentsql.New(s.db.SQL()).ReleaseByNumber(ctx, deploymentsql.ReleaseByNumberParams{ProjectID: projectID, Number: number})
	return s.read(ctx, row, err)
}

// ForProject is a project's releases, newest first.
func (s *Releases) ForProject(ctx context.Context, projectID ids.ID, limit int) ([]*Release, error) {
	raw, err := deploymentsql.New(s.db.SQL()).ReleasesForProject(ctx, deploymentsql.ReleasesForProjectParams{ProjectID: projectID, RowLimit: int64(limit)})
	rows, err := s.records(ctx, raw, err)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.State == ReleaseFailed {
			events, err := s.Events(ctx, r.ID)
			if err != nil {
				return nil, err
			}
			if len(events) > 0 {
				r.Failure = events[len(events)-1].Text
			}
		}
	}
	return rows, nil
}

// Recent is the listed projects' latest releases, newest first. The caller
// must have authorized the projects; an empty list selects nothing.
func (s *Releases) Recent(ctx context.Context, projectIDs []ids.ID, limit int) ([]*Release, error) {
	if len(projectIDs) == 0 {
		return []*Release{}, nil
	}
	raw, err := deploymentsql.New(s.db.SQL()).RecentReleases(ctx, deploymentsql.RecentReleasesParams{ProjectIds: projectIDs, RowLimit: int64(limit)})
	rows, err := s.records(ctx, raw, err)
	if err != nil {
		return nil, err
	}
	for _, release := range rows {
		if release.State != ReleaseFailed {
			continue
		}
		events, err := s.Events(ctx, release.ID)
		if err != nil {
			return nil, err
		}
		if len(events) > 0 {
			release.Failure = events[len(events)-1].Text
		}
	}
	return rows, nil
}

// Last is a project's newest release on a deployment, whatever its state.
func (s *Releases) Last(ctx context.Context, projectID ids.ID, deployment string) (*Release, error) {
	row, err := deploymentsql.New(s.db.SQL()).LastRelease(ctx, deploymentsql.LastReleaseParams{ProjectID: projectID, Deployment: deployment})
	return s.read(ctx, row, err)
}

// LastReleasedForDeployment reads successful history for this immutable
// placement. A replacement using its old name does not inherit that history.
func (s *Releases) LastReleasedForDeployment(ctx context.Context, deploymentID ids.ID) (*Release, error) {
	row, err := deploymentsql.New(s.db.SQL()).LatestReleasedForDeployment(ctx, deploymentsql.LatestReleasedForDeploymentParams{DeploymentID: deploymentID, State: Released.String()})
	return s.read(ctx, row, err)
}

// releaseForGeneration reads the release applied to this exact placement.
func (s *Service) releaseForGeneration(ctx context.Context, dep *Deployment) (*Release, error) {
	row, err := deploymentsql.New(s.db.SQL()).ReleaseForGeneration(ctx, deploymentsql.ReleaseForGenerationParams{DeploymentID: dep.ID, Generation: dep.ObservedGeneration})
	if found, err := database.Lookup(err); err != nil || !found {
		return nil, err
	}
	return s.Releases.read(ctx, row, nil)
}

// Live reports whether a release is the newest released on its recorded
// deployment identity while that deployment still stands. Historical releases
// without a recorded identity remain history; a name cannot establish liveness.
func (s *Releases) Live(ctx context.Context, rel *Release) (bool, error) {
	if rel.State != Released || rel.DeploymentID.IsZero() {
		return false, nil
	}
	q := deploymentsql.New(s.db.SQL())
	latest, err := q.LatestReleasedForDeployment(ctx, deploymentsql.LatestReleasedForDeploymentParams{DeploymentID: rel.DeploymentID, State: Released.String()})
	if found, err := database.Lookup(err); err != nil || !found {
		return false, err
	}
	if latest.ID != rel.ID {
		return false, nil
	}
	_, err = q.DeploymentByID(ctx, rel.DeploymentID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ReplacedBy is the number of the later release that replaced a released
// one on its deployment: the newest released there when that is newer. Zero
// when nothing later was released, including when the deployment was taken
// down with this release still the newest.
func (s *Releases) ReplacedBy(ctx context.Context, rel *Release) (int64, error) {
	if rel.State != Released || rel.DeploymentID.IsZero() {
		return 0, nil
	}
	latest, err := deploymentsql.New(s.db.SQL()).LatestReleasedForDeployment(ctx, deploymentsql.LatestReleasedForDeploymentParams{DeploymentID: rel.DeploymentID, State: Released.String()})
	if found, err := database.Lookup(err); err != nil || !found {
		return 0, err
	}
	if latest.ID == rel.ID || latest.Number <= rel.Number {
		return 0, nil
	}
	return latest.Number, nil
}

// LatestReleasedNumber is the newest released number, zero when none has been.
func (s *Releases) LatestReleasedNumber(ctx context.Context, projectID ids.ID, deployment string) (int64, error) {
	return deploymentsql.New(s.db.SQL()).LatestReleasedNumber(ctx, deploymentsql.LatestReleasedNumberParams{ProjectID: projectID, Deployment: deployment, State: Released.String()})
}

// ForChange is the releases that carried a change, newest first.
func (s *Releases) ForChange(ctx context.Context, projectID, changeID ids.ID) ([]*Release, error) {
	rows, err := deploymentsql.New(s.db.SQL()).ReleasesForChange(ctx, deploymentsql.ReleasesForChangeParams{ChangeID: changeID, ProjectID: projectID})
	return s.records(ctx, rows, err)
}

// Events is a release's log in the order it was written.
func (s *Releases) Events(ctx context.Context, releaseID ids.ID) ([]ReleaseEvent, error) {
	rows, err := deploymentsql.New(s.db.SQL()).ReleaseEvents(ctx, releaseID)
	if err != nil {
		return nil, err
	}
	var out []ReleaseEvent
	for _, r := range rows {
		out = append(out, ReleaseEvent{At: r.At.UTC().Format(time.RFC3339Nano), Text: r.Text})
	}
	return out, nil
}

// RepairInterruptedReleasesIn is the startup rule for releases the process
// that died owned: a release still releasing with no persisted deployment
// attempt is dead and says so in its log, while one with an attempt stays
// releasing for deployment recovery. Unclaimed requests owned by unfinished
// jobs remain queued. UPDATE RETURNING claims exactly the
// rows whose history is written, so a repeated repair cannot duplicate the
// receipt. It returns how many were failed and how many remain releasing.
// at is the caller's clock: when the dead releases' log lines are written.
func RepairInterruptedReleasesIn(ctx context.Context, tx *database.Tx, queuedReleaseIDs []ids.ID, at time.Time) (failed, durable int64, err error) {
	q := deploymentsql.New(tx.SQL())
	repaired, err := q.RepairInterruptedReleases(ctx, deploymentsql.RepairInterruptedReleasesParams{FailedState: ReleaseFailed.String(), OpenState: Releasing.String(), QueuedReleaseIds: queuedReleaseIDs, At: at})
	if err != nil {
		return 0, 0, fmt.Errorf("repairing interrupted releases and their history: %w", err)
	}
	// Each repaired release's history says why, written with the repair.
	rq := releasesql.New(tx.SQL())
	for _, id := range repaired {
		if _, err := rq.AppendReleaseEvent(ctx, releasesql.AppendReleaseEventParams{ID: ids.New(at), ReleaseID: id, At: at, Text: "AppTown restarted mid-release — this attempt is dead; press release again"}); err != nil {
			return 0, 0, fmt.Errorf("repairing interrupted releases and their history: %w", err)
		}
	}
	failed = int64(len(repaired))
	durable, err = q.ReleasingCount(ctx, Releasing.String())
	if err != nil {
		return 0, 0, fmt.Errorf("counting releases preserved for recovery: %w", err)
	}
	return failed, durable, nil
}

// BeforeBackups reads only explicit release associations within this project.
func (s *Releases) BeforeBackups(ctx context.Context, projectID ids.ID, backups []ids.ID) (map[ids.ID]int64, error) {
	rows, err := deploymentsql.New(s.db.SQL()).ReleasesBeforeBackups(ctx, deploymentsql.ReleasesBeforeBackupsParams{ProjectID: projectID, BackupIds: backups})
	if err != nil {
		return nil, err
	}
	out := map[ids.ID]int64{}
	for _, r := range rows {
		out[r.BackupID] = r.Number
	}
	return out, nil
}

// LandingRequests is the follows-main work requested by this change's landings,
// rather than every later release that also happens to contain the change.
func (s *Releases) LandingRequests(ctx context.Context, projectID, changeID ids.ID) ([]*Release, error) {
	raw, err := deploymentsql.New(s.db.SQL()).LandingRequestsForChange(ctx, deploymentsql.LandingRequestsForChangeParams{ProjectID: projectID, ChangeID: changeID})
	rows, err := s.records(ctx, raw, err)
	if err != nil {
		return nil, err
	}
	for _, rel := range rows {
		if rel.State == ReleaseFailed {
			events, err := s.Events(ctx, rel.ID)
			if err != nil {
				return nil, err
			}
			if len(events) > 0 {
				rel.Failure = events[len(events)-1].Text
			}
		}
	}
	return rows, nil
}

// RequestMetadata is the recorded authority and data evidence of a queued
// release. Credential names a credential record, never its secret value.
type RequestMetadata struct {
	Feed               string          `json:"feed"`
	SettingsRevision   int64           `json:"settingsRevision"`
	SetBy              string          `json:"setBy"`
	Setter             ids.ID          `json:"setter,omitzero"`
	Credential         string          `json:"credential"`
	Approvers          string          `json:"approvers"`
	BackupEvidence     json.RawMessage `json:"backupEvidence,omitempty"`
	UncopiedAcceptance json.RawMessage `json:"uncopiedAcceptance,omitempty"`
}

func (s *Releases) RequestMetadata(ctx context.Context, releaseID ids.ID) (*RequestMetadata, error) {
	row, err := deploymentsql.New(s.db.SQL()).ReleaseRequestMetadata(ctx, releaseID)
	if found, e := database.Lookup(err); e != nil || !found {
		return nil, e
	}
	out := &RequestMetadata{Approvers: row.LandingApprovers}
	if row.FeedRevision.Valid {
		out.Feed = "main"
		out.SettingsRevision = row.FeedRevision.Int64
	}
	if row.AuthorityJson != nil {
		principal, err := releasePrincipal(*row.AuthorityJson)
		if err != nil {
			return nil, err
		}
		out.SetBy, out.Setter, out.Credential = principal.Name, principal.ID, principal.Credential
	}
	if row.BackupEvidenceJson != nil {
		out.BackupEvidence = *row.BackupEvidenceJson
	}
	if row.UncopiedAcceptanceJson != nil {
		out.UncopiedAcceptance = *row.UncopiedAcceptanceJson
	}
	return out, nil
}

// NewChanges lists membership new since each release's previous successful
// release on the same deployment identity. Branch receipts are incremental, so
// earlier successful receipts also contribute to that boundary. Full membership
// stays immutable.
func (s *Releases) NewChanges(ctx context.Context, projectID ids.ID, releaseIDs []ids.ID) (map[ids.ID]map[ids.ID]bool, error) {
	rows, err := releasesql.New(s.db.SQL()).NewReleaseChanges(ctx, releasesql.NewReleaseChangesParams{ProjectID: projectID, ReleaseIds: releaseIDs})
	if err != nil {
		return nil, err
	}
	out := make(map[ids.ID]map[ids.ID]bool)
	for _, row := range rows {
		if out[row.ReleaseID] == nil {
			out[row.ReleaseID] = make(map[ids.ID]bool)
		}
		out[row.ReleaseID][row.ChangeID] = true
	}
	return out, nil
}

// ByID reads an immutable release identity.
func (s *Releases) ByID(ctx context.Context, id ids.ID) (*Release, error) {
	row, err := deploymentsql.New(s.db.SQL()).ReleaseByID(ctx, id)
	return s.read(ctx, row, err)
}
