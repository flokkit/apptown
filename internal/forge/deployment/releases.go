package deployment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/deployment/internal/deploymentsql"
	"flokkit.io/forge/internal/forge/deployment/internal/releasesql"
	"flokkit.io/forge/internal/stamp"
)

// A release (design/subsystems/deployment.md §2.8) is the institutional
// record of what shipped, where, as which generation: numbered per project,
// with the changes it carried and the timestamped log it wrote as it
// worked. Creation and settlement have their own owners; Releases is the
// ledger's read side and the startup repair of releases nobody owns.

// Release is one row of the release ledger.
type Release struct {
	ID          ids.ID       `json:"id"`
	ProjectID   ids.ID       `json:"projectId"`
	Number      int64        `json:"number"`
	State       ReleaseState `json:"state"`
	Deployment  string       `json:"deployment"`
	ExternalRef string       `json:"externalRef,omitempty"`
	ApprovedBy  string       `json:"approvedBy,omitempty"`
	RunID       ids.ID       `json:"runId,omitzero"`
	CreatedAt   string       `json:"createdAt"`
	ReleasedAt  stamp.Time   `json:"releasedAt,omitzero"`
	Notes       string       `json:"notes,omitempty"`
	CommitSHA   string       `json:"commitSha,omitempty"`
	SourceSHA   string       `json:"-"` // the tree a released image was built from
	// §2.8: what shipped, where, as which generation.
	BundleID     ids.ID `json:"bundleId,omitzero"`
	DeploymentID ids.ID `json:"deploymentId,omitzero"`
	Generation   int64  `json:"generation,omitempty"`
	ArtifactHash string `json:"artifactHash,omitempty"`
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
	clock func() time.Time // the forge's clock, which stamps a release; nil is the real one
}

func NewReleases(db *database.DB) *Releases { return &Releases{db: db} }

func (s *Releases) now() time.Time { return clock.Or(s.clock)() }

func releaseRecord(r deploymentsql.Release, err error) (*Release, error) {
	if err != nil {
		return nil, err
	}
	v := &Release{
		ID:           r.ID,
		ProjectID:    r.ProjectID,
		Number:       r.Number,
		Deployment:   r.Deployment,
		ExternalRef:  r.ExternalRef,
		ApprovedBy:   r.ApprovedBy,
		RunID:        r.RunID,
		CreatedAt:    r.CreatedAt.UTC().Format(time.RFC3339Nano),
		ReleasedAt:   stamp.Time{NullTime: r.ReleasedAt},
		Notes:        r.Notes,
		CommitSHA:    r.CommitSha,
		SourceSHA:    r.SourceSha,
		BundleID:     r.BundleID,
		DeploymentID: r.DeploymentID,
		Generation:   r.Generation,
		ArtifactHash: r.ArtifactHash,
	}
	if err := v.State.Scan(r.State); err != nil {
		return nil, err
	}
	return v, nil
}
func releaseRecords(rows []deploymentsql.Release, err error) ([]*Release, error) {
	if err != nil {
		return nil, err
	}
	var out []*Release
	for _, r := range rows {
		v, err := releaseRecord(r, nil)
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
	return releaseRecord(deploymentsql.New(s.db.SQL()).ReleaseByNumber(ctx, deploymentsql.ReleaseByNumberParams{ProjectID: projectID, Number: number}))
}

// ForProject is a project's releases, newest first.
func (s *Releases) ForProject(ctx context.Context, projectID ids.ID, limit int) ([]*Release, error) {
	return releaseRecords(deploymentsql.New(s.db.SQL()).ReleasesForProject(ctx, deploymentsql.ReleasesForProjectParams{ProjectID: projectID, RowLimit: int64(limit)}))
}

// Recent is the listed projects' latest releases, newest first. The caller
// must have authorized the projects; an empty list selects nothing.
func (s *Releases) Recent(ctx context.Context, projectIDs []ids.ID, limit int) ([]*Release, error) {
	if len(projectIDs) == 0 {
		return []*Release{}, nil
	}
	return releaseRecords(deploymentsql.New(s.db.SQL()).RecentReleases(ctx, deploymentsql.RecentReleasesParams{ProjectIds: projectIDs, RowLimit: int64(limit)}))
}

// Last is a project's newest release on a deployment, whatever its state.
func (s *Releases) Last(ctx context.Context, projectID ids.ID, deployment string) (*Release, error) {
	return releaseRecord(deploymentsql.New(s.db.SQL()).LastRelease(ctx, deploymentsql.LastReleaseParams{ProjectID: projectID, Deployment: deployment}))
}

// Live reports whether a release is what its deployment serves now: the
// newest released on that deployment, while the deployment still stands. A
// removed deployment's last release is history ("taken down" in its log),
// and a deployment made again under the same name is another deployment. A
// release from before releases recorded their deployment's identity is
// matched by the deployment's name.
func (s *Releases) Live(ctx context.Context, rel *Release) (bool, error) {
	if rel.State != Released {
		return false, nil
	}
	latest, err := s.LatestReleasedNumber(ctx, rel.ProjectID, rel.Deployment)
	if err != nil || latest != rel.Number {
		return false, err
	}
	q := deploymentsql.New(s.db.SQL())
	if !rel.DeploymentID.IsZero() {
		_, err = q.DeploymentByID(ctx, rel.DeploymentID)
	} else {
		_, err = q.DeploymentByName(ctx, deploymentsql.DeploymentByNameParams{ProjectID: rel.ProjectID, Name: rel.Deployment})
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// LatestReleasedNumber is the newest released number, zero when none has been.
func (s *Releases) LatestReleasedNumber(ctx context.Context, projectID ids.ID, deployment string) (int64, error) {
	return deploymentsql.New(s.db.SQL()).LatestReleasedNumber(ctx, deploymentsql.LatestReleasedNumberParams{ProjectID: projectID, Deployment: deployment, State: Released.String()})
}

// LatestReleasedOn is every project's live release on a deployment.
func (s *Releases) LatestReleasedOn(ctx context.Context, deployment string) ([]*Release, error) {
	return releaseRecords(deploymentsql.New(s.db.SQL()).LatestReleasedOn(ctx, deploymentsql.LatestReleasedOnParams{Deployment: deployment, State: Released.String()}))
}

// ForChange is the releases that carried a change, newest first.
func (s *Releases) ForChange(ctx context.Context, projectID, changeID ids.ID) ([]*Release, error) {
	return releaseRecords(deploymentsql.New(s.db.SQL()).ReleasesForChange(ctx, deploymentsql.ReleasesForChangeParams{ChangeID: changeID, ProjectID: projectID}))
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
	repaired, err := q.RepairInterruptedReleases(ctx, deploymentsql.RepairInterruptedReleasesParams{FailedState: ReleaseFailed.String(), OpenState: Releasing.String(), QueuedReleaseIds: queuedReleaseIDs})
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
