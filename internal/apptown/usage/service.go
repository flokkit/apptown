package usage

import (
	"context"
	"errors"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
)

// Service is the usage subsystem built once: the ledger of model usage
// and the inference request receipts.
type Service struct {
	Ledger    *Ledger
	Requests  *Requests
	authority access.ReadAuthorizer
}

// NewService builds the subsystem. authority answers Report's reads; a nil
// authority leaves Report unavailable, never open.
// now is AppTown's clock (nil the real one).
func NewService(db *database.DB, authority access.ReadAuthorizer, now func() time.Time) *Service {
	ledger, requests := New(db), NewRequests(db)
	ledger.Now, requests.Now = now, now
	return &Service{Ledger: ledger, Requests: requests, authority: authority}
}

// Query names one usage report: a window, optionally one project and one
// workflow.
type Query struct {
	WindowDays int
	ProjectID  ids.ID
	Workflow   string
}

// Report is the usage the verified caller may read. A named project needs
// project:read on it. Without one, a caller who reads projects across the
// instance sees the whole instance, including usage attributed to no
// project; anyone else, and every run or chat (bound to one project), sees
// only the projects they can read, so the totals and every breakdown leave
// other projects out.
func (s *Service) Report(ctx context.Context, q Query) (Report, error) {
	if s.authority == nil {
		return Report{}, access.ErrAuthorityUnavailable
	}
	if !q.ProjectID.IsZero() {
		res := access.Resource{Type: "project", ID: q.ProjectID.String(), ProjectID: q.ProjectID}
		if _, err := s.authority.Require(ctx, access.Permission{Capability: access.CapProjectRead, Resource: res, Context: access.Context{Route: "usage"}}); err != nil {
			return Report{}, err
		}
		return s.Ledger.summary(ctx, q.WindowDays, Scope{Projects: []ids.ID{q.ProjectID}}, q.Workflow, true)
	}
	// A run or a chat is bound to its project even when its person reads the
	// whole instance, and an instance decision does not check that binding.
	// Only a caller acting for no single project can read the whole instance.
	if caller, ok := access.CallerFrom(ctx); ok && caller.Principal.Kind != "instrument" {
		_, err := s.authority.Require(ctx, access.Permission{Capability: access.CapProjectRead, Resource: access.Resource{Type: "instance"}, Context: access.Context{Route: "usage"}})
		var denied *access.Denied
		switch {
		case err == nil:
			return s.Ledger.summary(ctx, q.WindowDays, Scope{Instance: true}, q.Workflow, false)
		case !errors.As(err, &denied):
			return Report{}, err
		}
	}
	idList, err := s.authority.VisibleProjects(ctx, access.CapProjectRead)
	if err != nil {
		return Report{}, err
	}
	return s.Ledger.summary(ctx, q.WindowDays, Scope{Projects: idList}, q.Workflow, false)
}
