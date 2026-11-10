package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/fault"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/stamp"
)

// A deployment (design/subsystems/deployment.md §2.3) is a project's app
// standing on one target under a name: identity, a kind, the binding, the
// handle the driver owns, generations, and the attributes that carry the
// nuance (§2.3a). This package owns the record and its rules; AppTown holds
// the doors and the drivers.

// AppliedAttempt identifies the observed generation’s successful attempt and finish time.
type AppliedAttempt struct {
	At         time.Time `json:"at"`
	Generation int64     `json:"generation"`
}

// Deployment includes the last successful attempt of its observed generation.
type Deployment struct {
	RuntimeState       string          `json:"runtimeState"`
	RuntimeActor       string          `json:"runtimeActor"`
	RuntimeAt          stamp.Time      `json:"runtimeAt,omitzero"`
	RuntimeError       string          `json:"runtimeError,omitempty"`
	actAt              time.Time       // the end of its last other act (see MarkActed)
	LastApplied        *AppliedAttempt `json:"lastApplied,omitempty"`
	DisplayLabel       string          `json:"displayLabel"`
	ID                 ids.ID          `json:"id"`
	ProjectID          ids.ID          `json:"projectId"`
	Kind               Kind            `json:"kind"`
	Name               string          `json:"name"`
	ChangeID           ids.ID          `json:"changeId,omitzero"`
	TargetID           ids.ID          `json:"targetId"`
	Handle             map[string]any  `json:"handle"`
	DesiredGeneration  int64           `json:"desiredGeneration"`
	ObservedGeneration int64           `json:"observedGeneration"`
	Readiness          Readiness       `json:"readiness"`
	PromotedFromID     ids.ID          `json:"promotedFromId,omitzero"`
	ApprovalRole       string          `json:"approvalRole"`
	FeedRevision       int64           `json:"feedRevision"`
	FeedConfirmed      bool            `json:"feedConfirmed"`
	feedAuthority      json.RawMessage
	Feed               string `json:"feed"`
	Audience           string `json:"audience"`
	DataClass          string `json:"data"`
	URL                string `json:"url"`
	// The built-in host's handle, as columns (the Docker driver reads them).
	HostPort  int    `json:"-"`
	Container string `json:"-"`
	// HostLabel is the name it carries on its host when that predates the
	// identity cutover (its old integer); "" means its UUID (Label).
	HostLabel   string `json:"-"`
	ReleaseRun  ids.ID `json:"releaseRun,omitzero"`
	ReleaseSHA  string `json:"releaseSha,omitempty"`
	ImageDigest string `json:"imageDigest,omitempty"`
	UpdatedAt   string `json:"updatedAt"`
	// StartFrom is the set a deployment started from a copy fills its data
	// from with its first release; StartedFrom is when that was done.
	StartFrom   ids.ID     `json:"startFrom,omitzero"`
	StartedFrom stamp.Time `json:"startedFrom,omitzero"`
	// RestoreCheck holds it after a whole-AppTown restore (restore_check.go);
	// "" when nothing does.
	RestoreCheck RestoreCheck `json:"restoreCheck,omitempty"`
}

// ReadingBasis names the settled deployment facts that observation depends on.
// admission, readiness and restore holds do not change what is running.
func (e *Deployment) ReadingBasis() any {
	return struct {
		ID, ProjectID, TargetID            ids.ID
		RuntimeState                       string
		RuntimeAt                          stamp.Time
		Name                               string
		Kind                               Kind
		Generation                         int64
		Handle                             map[string]any
		Container, HostLabel               string
		HostPort                           int
		ReleaseRun                         ids.ID
		ReleaseSHA, ImageDigest, DataClass string
	}{e.ID, e.ProjectID, e.TargetID, e.RuntimeState, e.RuntimeAt, e.Name, e.Kind, e.ObservedGeneration, e.Handle, e.Container, e.HostLabel, e.HostPort, e.ReleaseRun, e.ReleaseSHA, e.ImageDigest, e.DataClass}
}

// Placement is the record as a driver is handed it.
//
// A restored copy is always gated: the driver observes and takes it down
// as one, without its secret, which only the start holds (dataprotection, restored_copy.go).
// Label is the name the deployment carries on its host: container labels and
// boot proofs. A deployment made before the identity cutover keeps its old
// integer, which its containers carry; one made after uses its UUID.
func (e *Deployment) label() deploy.HostLabel {
	if e.HostLabel != "" {
		return deploy.HostLabel(e.HostLabel)
	}
	return deploy.HostLabel(e.ID.String())
}

func (e *Deployment) Placement() deploy.Placement {
	p := deploy.Placement{Stopped: e.RuntimeState == "stopped", ID: e.ID, Label: e.label(), Name: e.Name, Generation: e.ObservedGeneration,
		URL: e.URL, Handle: e.Handle, Container: e.Container, HostPort: e.HostPort}
	if e.Kind == Restored {
		p.Gate = &deploy.Gate{}
	}
	return p
}

// Protected is derived, never typed (§2.3a): a deployment is protected when
// its audience is outside the project or its data is production.
func (e *Deployment) Protected() bool {
	return e.Audience == "partners" || e.Audience == "public" || e.DataClass == "production"
}

// Attributes are the deployment attributes of §2.3a.
type Attributes struct {
	Feed      string `json:"feed"`
	Audience  string `json:"audience"`
	DataClass string `json:"data"`
}

// WithDefaults fills what a request left unsaid: a deployment is fed by
// hand, for the team, holding non-production data, until told
// otherwise.
func (at Attributes) WithDefaults() Attributes {
	if at.Feed == "" {
		at.Feed = "manual"
	}
	if at.Audience == "" {
		at.Audience = "team"
	}
	if at.DataClass == "" {
		at.DataClass = "non-production"
	}
	return at
}

func (at Attributes) Validate() error {
	switch at.Feed {
	case "main", "promotion", "manual":
	default:
		return fmt.Errorf("feed must be main, promotion or manual (got %q)", at.Feed)
	}
	switch at.Audience {
	case "team", "organisation", "partners", "public":
	default:
		return fmt.Errorf("audience must be team, organisation, partners or public (got %q)", at.Audience)
	}
	switch at.DataClass {
	case "synthetic", "non-production", "production":
	default:
		return fmt.Errorf("data must be synthetic, non-production or production (got %q)", at.DataClass)
	}
	return nil
}

var (
	deploymentNameRe      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	errDeploymentName     = errors.New("a deployment name is a short lowercase word")
	errDeploymentAdmitted = fault.New(fault.Conflict, "the deployment has an admitted generation; changing its target or making it protected requires a migration")
)

// PreviewName is a preview deployment's name: one per change.
func PreviewName(changeID ids.ID) string { return fmt.Sprintf("preview-%d", changeID) }

// Deployments is the supported record API. Lifecycle writes (attempts,
// outcomes, recovery) have their own owners; this is identity, binding and
// attributes.
type Deployments struct {
	db    *database.DB
	clock func() time.Time // AppTown's clock, which stamps a deployment's updates; nil is the real one
}

func (s *Deployments) now() time.Time { return clock.Or(s.clock)().UTC() }

func NewDeployments(db *database.DB) *Deployments { return &Deployments{db: db} }

func deploymentRecord(r deploymentsql.Deployment, err error) (*Deployment, error) {
	if err != nil {
		return nil, err
	}
	e := &Deployment{
		RuntimeState: r.RuntimeState, RuntimeActor: r.RuntimeActor, RuntimeAt: stamp.Time{NullTime: r.RuntimeAt}, RuntimeError: r.RuntimeError, actAt: r.ActAt.Time,
		ID:                 r.ID,
		ProjectID:          r.ProjectID,
		Name:               r.Name,
		DisplayLabel:       r.DisplayLabel,
		ChangeID:           r.ChangeID,
		TargetID:           r.TargetID,
		Handle:             jsonMap(string(r.HandleJson)),
		DesiredGeneration:  r.DesiredGeneration,
		ObservedGeneration: r.ObservedGeneration,
		Feed:               r.Feed,
		PromotedFromID:     r.PromotedFromID,
		FeedRevision:       r.FeedRevision,
		ApprovalRole:       r.ApprovalRole,
		FeedConfirmed:      r.FeedAuthorityJson != nil,
		feedAuthority:      feedAuthority(r.FeedAuthorityJson),
		Audience:           r.Audience,
		DataClass:          r.DataClass,
		URL:                r.Url,
		HostPort:           int(r.HostPort),
		Container:          r.Container,
		HostLabel:          r.HostLabel,
		ReleaseRun:         r.ReleaseRun,
		ReleaseSHA:         r.ReleaseSha,
		ImageDigest:        r.ImageDigest,
		UpdatedAt:          r.UpdatedAt.UTC().Format(time.RFC3339Nano),
		StartFrom:          r.StartFromBackup,
		StartedFrom:        stamp.Time{NullTime: r.StartedFromAt},
		RestoreCheck:       RestoreCheck(r.RestoreCheck),
	}
	if err := e.Kind.Scan(r.Kind); err != nil {
		return nil, err
	}
	if err := e.Readiness.Scan(r.Readiness); err != nil {
		return nil, err
	}
	return e, nil
}

// MarkActed records that an act other than an applied attempt or a runtime
// control (a restore) has just ended on this deployment's runtime, so its
// parts wait for a reading begun after it.
func (s *Deployments) MarkActed(ctx context.Context, id ids.ID) error {
	return markActedIn(ctx, s.db.SQL(), id, s.now())
}

func markActedIn(ctx context.Context, db deploymentsql.DBTX, id ids.ID, at time.Time) error {
	_, err := deploymentsql.New(db).MarkDeploymentAct(ctx, deploymentsql.MarkDeploymentActParams{ID: id, At: at})
	return err
}

func (s *Deployments) ByID(ctx context.Context, id ids.ID) (*Deployment, error) {
	return deploymentByIDRecord(deploymentsql.New(s.db.SQL()).DeploymentByID(ctx, id))
}

func (s *Deployments) ByName(ctx context.Context, projectID ids.ID, name string) (*Deployment, error) {
	return deploymentByNameRecord(deploymentsql.New(s.db.SQL()).DeploymentByName(ctx, deploymentsql.DeploymentByNameParams{ProjectID: projectID, Name: name}))
}

// ForProject lists a project's deployments, standing first.
func (s *Deployments) ForProject(ctx context.Context, projectID ids.ID) ([]*Deployment, error) {
	rows, err := deploymentsql.New(s.db.SQL()).DeploymentsForProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	var out []*Deployment
	for _, r := range rows {
		e, err := appliedDeploymentRecord(r.Deployment, r.LastAppliedAt, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// lockDeploymentRecordIn reads a deployment under its row lock inside a caller's
// transaction: the serialization point for admission and outcomes.
func lockDeploymentRecordIn(ctx context.Context, tx *database.Tx, id ids.ID) (*Deployment, error) {
	return deploymentRecord(deploymentsql.New(tx.SQL()).DeploymentForUpdate(ctx, id))
}

// EnsureStanding records a standing deployment on a target with its
// attributes, leaving the handle and generations of an existing row alone:
// attributes are settings, the rest is state. Binding and protection are
// fixed once a generation has been admitted; the upsert checks under its
// row lock so concurrent admission cannot slip between a read and an update.
func (s *Deployments) EnsureStanding(ctx context.Context, projectID ids.ID, name string, targetID ids.ID, at Attributes) (*Deployment, error) {
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*Deployment, error) {
		return ensureStandingIn(ctx, tx, projectID, name, targetID, at, s.now())
	})
}

func validateStanding(name string, at Attributes) error {
	if !deploymentNameRe.MatchString(name) {
		return fmt.Errorf("%w (got %q)", errDeploymentName, name)
	}
	if strings.HasPrefix(name, "preview") {
		return errors.New("preview is the name of a kind, not of a standing deployment")
	}
	// copy-<n> names a restored copy: its app and cluster are found by that
	// name when it is taken down, so no standing deployment may share it.
	if strings.HasPrefix(name, "copy-") {
		return errors.New("copy-<n> is the name of a restored copy, not of a standing deployment")
	}
	if err := at.Validate(); err != nil {
		return err
	}
	return nil
}

func ensureStandingIn(ctx context.Context, tx *database.Tx, projectID ids.ID, name string, targetID ids.ID, at Attributes, now time.Time) (*Deployment, error) {
	if err := validateStanding(name, at); err != nil {
		return nil, err
	}
	{
		if err := lockAvailableTargetIn(ctx, tx, targetID); err != nil {
			return nil, err
		}
		if err := nameFreeOfDataIn(ctx, tx, projectID, name, targetID); err != nil {
			return nil, err
		}
		e, err := deploymentRecord(deploymentsql.New(tx.SQL()).EnsureStanding(ctx, deploymentsql.EnsureStandingParams{ID: ids.New(now.UTC()),
			ProjectID: projectID,
			Name:      name,
			Kind:      Standing.String(),
			TargetID:  targetID,
			Feed:      at.Feed,
			Audience:  at.Audience,
			DataClass: at.DataClass,
			UpdatedAt: now.UTC(),
		}))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%s: %w", name, errDeploymentAdmitted)
		}
		if err != nil {
			return nil, err
		}
		if err := preserveDefaultConfiguration(ctx, tx, e); err != nil {
			return nil, err
		}
		return e, nil
	}
}

// RecordPreview records a preview as a row while it runs (§2.3: previews
// are rows, unique by change, outside the ladder), on the given target.
// created replaces the previous boot's resource list atomically with the row;
// nil means the running preview was reused, so its existing list stays.
// Preview naming belongs to its boot, never the placement's deployment key.
func (s *Deployments) RecordPreview(ctx context.Context, projectID, changeID, targetID ids.ID, container string, hostPort int, url string, created []deploy.Owned) error {
	ctx = context.WithoutCancel(ctx) // the preview runs: its row is written though ctx has ended
	now := s.now()
	handle, _ := json.Marshal(map[string]any{"container": container, "hostPort": hostPort})
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		q := deploymentsql.New(tx.SQL())
		row, err := q.RecordPreview(ctx, deploymentsql.RecordPreviewParams{ID: ids.New(now),
			ProjectID:  projectID,
			Name:       PreviewName(changeID),
			Kind:       preview.String(),
			ChangeID:   changeID,
			TargetID:   targetID,
			HandleJson: json.RawMessage(string(handle)),
			HostPort:   int64(hostPort),
			Container:  container,
			Url:        url,
			UpdatedAt:  now,
		})
		if err != nil || created == nil {
			return err
		}
		previous, err := ownedIn(ctx, q, row.ID)
		if err != nil {
			return err
		}
		for _, o := range previous {
			if _, err := q.ForgetOwned(ctx, deploymentsql.ForgetOwnedParams{DeploymentID: row.ID, Kind: o.Kind, Ident: o.ID}); err != nil {
				return err
			}
		}
		for _, o := range created {
			if _, err := q.RecordOwned(ctx, deploymentsql.RecordOwnedParams{ID: ids.New(now), DeploymentID: row.ID, Kind: o.Kind, Ident: o.ID, Name: o.Name, HoldsData: o.Data, Need: o.Need, RecordedAt: now}); err != nil {
				return err
			}
		}
		return nil
	})
}

// ForgetPreview drops a change's preview row: the container it named is gone.
func (s *Deployments) ForgetPreview(ctx context.Context, changeID ids.ID) error {
	ctx = context.WithoutCancel(ctx) // the preview is removed: its row goes though ctx has ended
	_, err := deploymentsql.New(s.db.SQL()).ForgetPreview(ctx, deploymentsql.ForgetPreviewParams{Kind: preview.String(), ChangeID: changeID})
	return err
}

// ErrNameHeldByData refuses a new deployment whose name, on its target,
// still has data an earlier deployment of that name left there (kept where
// the target has no backups, or a delete that failed). A Docker host would
// otherwise mount the old data volume silently. Settling each row released,
// once the data is deleted by hand, frees the name.
var ErrNameHeldByData = fault.New(fault.Conflict, "data from an earlier deployment of this name is still at the target")

// nameFreeOfDataIn refuses to make a deployment under a name whose earlier
// deployment left data at this target. An existing deployment of that name
// (a rebind or a settings change) is not a new one.
func nameFreeOfDataIn(ctx context.Context, tx *database.Tx, projectID ids.ID, name string, targetID ids.ID) error {
	q := deploymentsql.New(tx.SQL())
	if _, err := q.DeploymentByName(ctx, deploymentsql.DeploymentByNameParams{ProjectID: projectID, Name: name}); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rows, err := q.OpenRetainedDataForName(ctx, deploymentsql.OpenRetainedDataForNameParams{ProjectID: projectID, Deployment: name, TargetID: targetID, OpenState: RetainedOpen.String()})
	if err != nil || len(rows) == 0 {
		return err
	}
	var what []string
	for _, r := range rows {
		what = append(what, r.Need)
	}
	return fmt.Errorf("%w: %s (%s); delete it and mark it deleted under Left behind, or choose another name", ErrNameHeldByData, name, strings.Join(what, ", "))
}

// completedAttempt selects successful application evidence for release inputs.
func (s *Deployments) completedAttempt(ctx context.Context, d *Deployment) (deploymentsql.DeploymentAttempt, bool, error) {
	if d.ObservedGeneration == 0 {
		return deploymentsql.DeploymentAttempt{}, false, nil
	}
	at, err := deploymentsql.New(s.db.SQL()).CompletedAttempt(ctx, deploymentsql.CompletedAttemptParams{DeploymentID: d.ID, Generation: d.ObservedGeneration, Status: AttemptDone.String()})
	found, err := database.Lookup(err)
	return at, found, err
}

// The successful attempt is selected in the same statement as the deployment.
func appliedDeploymentRecord(row deploymentsql.Deployment, at sql.NullTime, err error) (*Deployment, error) {
	d, err := deploymentRecord(row, err)
	if err != nil {
		return nil, err
	}
	if at.Valid {
		d.LastApplied = &AppliedAttempt{At: at.Time, Generation: d.ObservedGeneration}
	}
	return d, nil
}

func deploymentByIDRecord(row deploymentsql.DeploymentByIDRow, err error) (*Deployment, error) {
	return appliedDeploymentRecord(row.Deployment, row.LastAppliedAt, err)
}

func deploymentByNameRecord(row deploymentsql.DeploymentByNameRow, err error) (*Deployment, error) {
	return appliedDeploymentRecord(row.Deployment, row.LastAppliedAt, err)
}

func feedAuthority(raw *json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return *raw
}
