package conformance

import (
	"context"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

// Action identifies a supported operation; authority maps it to current policy.
type Action uint8

const (
	ReadFleet Action = iota + 1
	ReadProject
	RunReview
	ConfirmFlag
	CreateException
	RevokeException
	ClearHistory
	// RunWhatIf admits a what-if: the same grant as a review for a person,
	// but a distinct act, so a scope that may request a project's standing
	// review (an adoption's, a remediation's) cannot judge other versions.
	RunWhatIf
)

type Permission struct {
	Action Action
	// ProjectID scopes a project operation. Projects scopes review admission;
	// nil means the fleet, an explicit empty selection means no projects.
	ProjectID ids.ID
	Projects  []ids.ID
}

// Authority returns the authenticated actor for receipts. An actor supplied in
// a command is never authority. Missing context or an unavailable decision fails.
type operationAuthority interface {
	Actor(context.Context) (string, error)
	Require(context.Context, Permission) (string, error)
}

var ErrCallerRequired = &access.Denied{Code: "caller_required", Reason: "conformance requires an authenticated operation context"}
var ErrScope = fault.New(fault.Conflict, "operation is outside this review's admitted scope")
var ErrSource = errors.New("committed project source is unavailable; run the review again when it is available")

type SourceHead struct{ SHA, Unavailable string }
type ProjectSource struct {
	ID           ids.ID
	Slug, Branch string
}

// CheckSource supplies pinned declarations through the caller's database snapshot.
// Adoption owns the selections; Conformance owns interpreting their evaluators.
type CheckVersion struct {
	Block   string `json:"block"`
	Version int    `json:"version"`
}

type CheckSource interface {
	HistoricalChecksIn(context.Context, database.Queryer, []CheckVersion) ([]DeclaredCheck, error)
	ChecksIn(context.Context, database.Queryer, []ids.ID) (map[ids.ID][]DeclaredCheck, error)
}

// Source is the two inputs of a review: its repository head and its checks.
type Source interface {
	CheckSource
	Head(context.Context, ProjectSource) (SourceHead, error)
}

// Service is the supported internal API. Persistence is private and transports
// supply authority through the same port. Source work happens outside SQL locks.
type Service struct {
	db        *database.DB
	records   reviewStore
	authority operationAuthority
	source    Source
}

func (s *Service) require(ctx context.Context, p Permission) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s == nil || s.authority == nil {
		return "", ErrCallerRequired
	}
	who, err := s.authority.Require(ctx, p)
	if err != nil {
		return "", err
	}
	if who == "" {
		return "", ErrCallerRequired
	}
	return who, nil
}

// Resolve identity before a point lookup whose project is not known yet.
func (s *Service) caller(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s == nil || s.authority == nil {
		return "", ErrCallerRequired
	}
	who, err := s.authority.Actor(ctx)
	if err != nil {
		return "", err
	}
	if who == "" {
		return "", ErrCallerRequired
	}
	return who, nil
}

func (s *Service) sourceHead(ctx context.Context, p Project) (SourceHead, error) {
	if s.source == nil {
		return SourceHead{Unavailable: ErrSource.Error()}, nil
	}
	head, err := s.source.Head(ctx, ProjectSource{ID: p.ID, Slug: p.Slug, Branch: p.Branch})
	if err != nil {
		return SourceHead{}, err
	}
	return head, nil
}

func (s *Service) Reviews(ctx context.Context, limit int) ([]ReviewSummary, error) {
	if _, err := s.require(ctx, Permission{Action: ReadFleet}); err != nil {
		return nil, err
	}
	return s.records.summaries(ctx, limit)
}
func (s *Service) Review(ctx context.Context, id ids.ID) (*ReviewDetail, error) {
	if _, err := s.require(ctx, Permission{Action: ReadFleet}); err != nil {
		return nil, err
	}
	return s.records.detail(ctx, id)
}
func (s *Service) ProjectHistory(ctx context.Context, id ids.ID, limit int) ([]HistoryPoint, error) {
	if _, err := s.require(ctx, Permission{Action: ReadProject, ProjectID: id}); err != nil {
		return nil, err
	}
	return s.records.projectHistory(ctx, id, limit)
}
func (s *Service) ProjectFixes(ctx context.Context, id ids.ID, limit int) ([]Fix, error) {
	if _, err := s.require(ctx, Permission{Action: ReadProject, ProjectID: id}); err != nil {
		return nil, err
	}
	return s.records.projectFixes(ctx, id, limit)
}
func (s *Service) LatestStanding(ctx context.Context, id ids.ID, slug string) (*RecordedStanding, error) {
	if _, err := s.require(ctx, Permission{Action: ReadProject, ProjectID: id}); err != nil {
		return nil, err
	}
	return s.records.latestStanding(ctx, id, slug)
}
func (s *Service) Dispositions(ctx context.Context, id ids.ID, active bool) ([]Disposition, error) {
	if _, err := s.require(ctx, Permission{Action: ReadProject, ProjectID: id}); err != nil {
		return nil, err
	}
	return readDispositions(ctx, s.records.db, []ids.ID{id}, active, s.records.now())
}
func (s *Service) AdoptedChecks(ctx context.Context, id ids.ID) ([]DeclaredCheck, error) {
	if _, err := s.require(ctx, Permission{Action: ReadProject, ProjectID: id}); err != nil {
		return nil, err
	}
	checks, err := readAdoptedChecks(ctx, s.records.db, s.records.checks, []ids.ID{id})
	if err != nil {
		return nil, err
	}
	return checks[id], nil
}

type Confirmation struct {
	VerdictID ids.ID
	Note      string
}

func (s *Service) Confirm(ctx context.Context, in Confirmation) (*Adjudication, error) {
	who, err := s.require(ctx, Permission{Action: ConfirmFlag})
	if err != nil {
		return nil, err
	}
	if in.VerdictID.IsZero() {
		return nil, fmt.Errorf("verdict id must be positive")
	}
	return s.records.confirm(ctx, confirmationRequest{VerdictID: in.VerdictID, By: who, Note: in.Note})
}

// ErrCheckNotAdopted is a check the project does not adopt now: its block
// was removed or turned off, or never declared the check.
var ErrCheckNotAdopted = fault.New(fault.Unprocessable, "the project does not adopt that check")

// AdoptedCheck distinguishes a removed declaration (ErrCheckNotAdopted) from
// an unavailable catalog (the read's own failure).
func (s *Service) AdoptedCheck(ctx context.Context, projectID ids.ID, block, name string) (DeclaredCheck, error) {
	checks, err := s.AdoptedChecks(ctx, projectID)
	if err != nil {
		return DeclaredCheck{}, err
	}
	for _, check := range checks {
		if check.Block == block && check.Check.Name == name {
			return check, nil
		}
	}
	return DeclaredCheck{}, fmt.Errorf("%s · %s: %w", block, name, ErrCheckNotAdopted)
}
