package adrs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/adrs/internal/adrssql"
	"flokkit.io/forge/internal/forge/identity"

	"flokkit.io/forge/internal/database"
)

type Action uint8

const (
	Identify Action = iota + 1
	Read
	Write
)

type authorization struct {
	Action    Action
	ProjectID ids.ID
}

var ErrCallerRequired = fault.New(fault.Unauthenticated, "ADR operations require explicit caller authority")

type Service struct {
	records   store
	authority access.ReadAuthorizer
	changed   func(Record)
}

// New is the decision records' owner; now is AppTown's clock, which stamps
// each record and receipt (the real clock when nil).
func New(db *database.DB, authority access.ReadAuthorizer, changed func(Record), now func() time.Time) *Service {
	return &Service{records: store{db: db, clock: now}, authority: authority, changed: changed}
}
func (s *Service) require(ctx context.Context, p authorization) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s == nil || s.authority == nil {
		return "", ErrCallerRequired
	}

	caller, ok := access.CallerFrom(ctx)
	if !ok || !caller.Verified || caller.Principal.Name == "" {
		return "", ErrCallerRequired
	}
	pr := caller.Principal
	if p.Action == Identify {
		if err := identity.RequireEnabledIn(ctx, s.records.db, pr); err != nil {
			return "", err
		}
		return pr.Name, nil
	}
	capability := access.CapProjectRead
	if p.Action == Write {
		capability = access.CapChangeAttach
	} else if p.Action != Read {
		return "", &access.Denied{Code: access.DenyOutsideScope, Reason: "unknown ADR operation"}
	}
	if pr.Kind == "instrument" && (p.ProjectID.IsZero() || p.ProjectID != pr.ProjectID) {
		return "", &access.Denied{Code: access.DenyOutsideScope, Reason: "this session may access only its project's decisions"}
	}
	resource := access.Resource{Type: "instance"}
	if !p.ProjectID.IsZero() {
		resource = access.Resource{Type: "project", ID: p.ProjectID.String(), ProjectID: p.ProjectID}
	}
	caller, err := s.authority.Require(ctx, access.Permission{Capability: capability, Resource: resource, Context: access.Context{Route: "adrs"}})
	if errors.Is(err, access.ErrPrincipalRequired) {
		return "", ErrCallerRequired
	}
	if err == nil && caller.Principal.Name == "" {
		err = ErrCallerRequired
	}
	return caller.Principal.Name, err
}
func (s *Service) scope(ctx context.Context, id ids.ID, act Action) (ids.ID, string, error) {
	if _, err := s.require(ctx, authorization{Action: Identify}); err != nil {
		return ids.Nil, "", err
	}
	project, err := readProjectID(ctx, s.records.db, id)
	if err != nil {
		return ids.Nil, "", err
	}
	who, err := s.require(ctx, authorization{Action: act, ProjectID: project})
	return project, who, err
}

// ProjectID resolves only routing scope; reading the record still requires Read.
func (s *Service) ProjectID(ctx context.Context, id ids.ID) (ids.ID, error) {
	if _, err := s.require(ctx, authorization{Action: Identify}); err != nil {
		return ids.Nil, err
	}
	return readProjectID(ctx, s.records.db, id)
}
func (s *Service) Get(ctx context.Context, id ids.ID) (*Detail, error) {
	project, _, err := s.scope(ctx, id, Read)
	if err != nil {
		return nil, err
	}
	tx, err := s.records.db.ReadSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	v, err := readRecord(ctx, tx, id, project, false)
	if err != nil {
		return nil, err
	}
	next, err := queries(tx).SuccessorID(ctx, adrssql.SuccessorIDParams{SupersedesID: id, ProjectID: project, Status: Accepted.String()})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Detail{ADR: v, SupersededByID: next, Options: v.Status.options()}, nil
}
func (s *Service) List(ctx context.Context, f Filter) ([]Record, error) {
	if _, err := s.require(ctx, authorization{Action: Identify}); err != nil {
		return nil, err
	}
	var idList []ids.ID
	if !f.ProjectID.IsZero() {
		if _, err := s.require(ctx, authorization{Action: Read, ProjectID: f.ProjectID}); err != nil {
			return nil, err
		}
		idList = []ids.ID{f.ProjectID}
	} else {
		var err error
		idList, err = s.authority.VisibleProjects(ctx, access.CapProjectRead)
		if err != nil {
			return nil, err
		}
	}
	return readRecords(ctx, s.records.db, f, idList)
}
func (s *Service) ForChange(ctx context.Context, projectID, changeID ids.ID) ([]ChangeSummary, error) {
	if projectID.IsZero() || changeID.IsZero() {
		return nil, ErrInput
	}
	if _, err := s.require(ctx, authorization{Action: Read, ProjectID: projectID}); err != nil {
		return nil, err
	}
	return readChangeSummaries(ctx, s.records.db, projectID, changeID)
}
func (s *Service) Counts(ctx context.Context, idList []ids.ID) (map[ids.ID]int, error) {
	if _, err := s.require(ctx, authorization{Action: Identify}); err != nil {
		return nil, err
	}
	out := make(map[ids.ID]int, len(idList))
	if len(idList) == 0 {
		return out, nil
	}
	for _, id := range idList {
		if id.IsZero() {
			return nil, ErrInput
		}
	}
	visible, err := s.authority.VisibleProjects(ctx, access.CapProjectRead)
	if err != nil {
		return nil, err
	}
	permitted := make(map[ids.ID]bool, len(visible))
	for _, id := range visible {
		permitted[id] = true
	}
	selected := make([]ids.ID, 0, len(idList))
	for _, id := range idList {
		if permitted[id] {
			selected = append(selected, id)
		}
	}
	rows, err := queries(s.records.db).ADRCountsByProject(ctx, selected)
	if err != nil {
		return nil, err
	}
	for _, v := range rows {
		out[v.ProjectID] = int(v.Count)
	}
	return out, nil
}
func (s *Service) Create(ctx context.Context, in Create) (*Record, error) {
	if in.ProjectID.IsZero() {
		return nil, ErrInput
	}
	who, err := s.require(ctx, authorization{Action: Write, ProjectID: in.ProjectID})
	if err != nil {
		return nil, err
	}
	v, err := s.records.create(ctx, in, who)
	if err != nil {
		return nil, err
	}
	return &v, nil
}
func (s *Service) Edit(ctx context.Context, in Edit) (*Record, error) {
	if in.ID.IsZero() || in.Decision >= retire {
		return nil, fmt.Errorf("%w: unsupported public decision", ErrInput)
	}
	project, who, err := s.scope(ctx, in.ID, Write)
	if err != nil {
		return nil, err
	}
	v, err := s.records.edit(ctx, project, in, who)
	if err != nil {
		return nil, err
	}
	if s.changed != nil {
		s.changed(v)
	}
	return &v, nil
}
func (s *Service) ProposeSuccessor(ctx context.Context, in Successor) (*Record, error) {
	project, who, err := s.scope(ctx, in.ID, Write)
	if err != nil {
		return nil, err
	}
	v, err := s.records.successor(ctx, project, in, who)
	if err != nil {
		return nil, err
	}
	return &v, nil
}
