package deployment

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// RoutineUpdates governs releases accepted under standing approval. Days names
// comma-separated weekdays (sun through sat); From and To are local HH:MM.
type RoutineUpdates struct {
	Mode string `json:"mode"`
	Days string `json:"days,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	Zone string `json:"zone,omitempty"`
}

func (r RoutineUpdates) validate() error {
	if r.Mode != "automatic" && r.Mode != "person" && r.Mode != "window" {
		return fault.New(fault.Invalid, "choose automatic, window or person for routine updates")
	}
	if r.Mode != "window" {
		if r.Days != "" || r.From != "" || r.To != "" || r.Zone != "" {
			return fault.New(fault.Invalid, "only a release window has days and times")
		}
		return nil
	}
	if _, err := time.LoadLocation(r.Zone); err != nil || r.Zone == "" {
		return fault.New(fault.Invalid, "choose a valid time zone")
	}
	for _, clock := range []string{r.From, r.To} {
		if _, err := time.Parse("15:04", clock); err != nil {
			return fault.New(fault.Invalid, "window times must be HH:MM")
		}
	}
	if r.From == r.To {
		return fault.New(fault.Invalid, "a window must have different start and end times")
	}
	if r.Days == "" {
		return fault.New(fault.Invalid, "choose the window's days")
	}
	for _, day := range strings.Split(r.Days, ",") {
		if !strings.Contains(",sun,mon,tue,wed,thu,fri,sat,", ","+day+",") {
			return fault.New(fault.Invalid, "window days must be sun, mon, tue, wed, thu, fri or sat")
		}
	}
	return nil
}

// nextWindow returns now inside a selected window, otherwise its next start.
// Constructing each local date preserves the selected wall time across DST.
func (r RoutineUpdates) nextWindow(now time.Time) time.Time {
	zone, _ := time.LoadLocation(r.Zone)
	local := now.In(zone)
	from, _ := time.Parse("15:04", r.From)
	to, _ := time.Parse("15:04", r.To)
	for offset := -1; offset <= 7; offset++ {
		date := local.AddDate(0, 0, offset)
		day := strings.ToLower(date.Weekday().String()[:3])
		if !strings.Contains(","+r.Days+",", ","+day+",") {
			continue
		}
		start := time.Date(date.Year(), date.Month(), date.Day(), from.Hour(), from.Minute(), 0, 0, zone)
		end := time.Date(date.Year(), date.Month(), date.Day(), to.Hour(), to.Minute(), 0, 0, zone)
		if !end.After(start) {
			end = end.AddDate(0, 0, 1)
		}
		if !now.Before(start) && now.Before(end) {
			return now
		}
		if start.After(now) {
			return start
		}
	}
	panic("validated window has no next start")
}

type RoutineUpdateSettings struct {
	Revision       int64          `json:"revision"`
	RoutineUpdates RoutineUpdates `json:"routineUpdates"`
}

func (s *Service) SetRoutineUpdates(ctx context.Context, pid ids.ID, name string, in RoutineUpdateSettings) (*Deployment, error) {
	if err := in.RoutineUpdates.validate(); err != nil {
		return nil, err
	}
	if s.configuration.Authority == nil {
		return nil, access.ErrAuthorityUnavailable
	}
	actor, err := s.configuration.Authority.Require(ctx, access.Permission{Capability: access.CapDeploymentBind, Resource: access.Resource{Type: "project", ID: pid.String(), ProjectID: pid}, Context: access.Context{Consequential: true, Route: "deployment.routine-updates"}})
	if err != nil {
		return nil, err
	}
	dep, err := s.Deployments.ByName(ctx, pid, name)
	if err != nil {
		return nil, err
	}
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		if err := LockProjectIn(ctx, tx, pid); err != nil {
			return err
		}
		if err := project.RefuseUnlessActiveIn(ctx, tx, pid); err != nil {
			return err
		}
		current, err := lockDeploymentRecordIn(ctx, tx, dep.ID)
		if err != nil {
			return err
		}
		if current.RoutineUpdatesRevision != in.Revision {
			return fault.New(fault.Conflict, "routine update settings changed; reload before saving")
		}
		raw, err := json.Marshal(in.RoutineUpdates)
		if err != nil {
			return err
		}
		n, err := deploymentsql.New(tx.SQL()).SetRoutineUpdates(ctx, deploymentsql.SetRoutineUpdatesParams{ID: dep.ID, RoutineUpdates: raw, Revision: in.Revision})
		if err != nil {
			return err
		}
		if n != 1 {
			return fault.New(fault.Conflict, "routine update settings changed")
		}
		return audit.At(s.now()).RecordIn(ctx, tx, actor.Principal.Name, pid, "deployment.routine-updates", fmt.Sprintf("%s/%s", pid, name), in)
	})
	if err != nil {
		return nil, err
	}
	return s.Deployments.ByID(ctx, dep.ID)
}

func (s *Service) routineReleaseIn(ctx context.Context, q database.Queryer, dep *Deployment) (bool, time.Time, string, error) {
	holds, err := s.HeldBackIn(ctx, q, dep.ProjectID)
	if err != nil {
		return false, time.Time{}, "", err
	}
	for _, hold := range holds {
		if hold.DeploymentID == dep.ID {
			return true, time.Time{}, "waiting for a person: " + hold.String(), nil
		}
	}
	if dep.Protected() {
		return true, time.Time{}, "waiting for a person to approve this protected deployment", nil
	}
	switch dep.RoutineUpdates.Mode {
	case "person":
		return true, time.Time{}, "waiting for a person to release this update", nil
	case "window":
		return false, dep.RoutineUpdates.nextWindow(s.now()), "", nil
	case "automatic":
		return false, time.Time{}, "", nil
	default:
		return false, time.Time{}, "", fmt.Errorf("unknown routine update setting %q", dep.RoutineUpdates.Mode)
	}
}
