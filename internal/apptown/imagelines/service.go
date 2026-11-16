// Package imagelines owns instance-wide registry readings. A reading is reused
// for a day; a failed refresh may use it for seven days, explicitly stale.
package imagelines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	q "github.com/flokkit/apptown/internal/apptown/imagelines/internal/imagelinesql"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/imageline"
	"github.com/flokkit/apptown/internal/trust"
	"golang.org/x/sync/singleflight"
)

type Reading struct {
	imageline.Answer
	ReadAt time.Time `json:"readAt"`
	Stale  bool      `json:"stale"`
	Error  string    `json:"error,omitempty"`
}

func (r Reading) Evidence() string {
	if r.Stale {
		return fmt.Sprintf("registry unreachable; used the reading from %s: %s", r.ReadAt.Format(time.RFC3339), r.Error)
	}
	return "registry reading from " + r.ReadAt.Format(time.RFC3339)
}

type Resolver interface {
	Answer(context.Context, string, imageline.Scope) (Reading, error)
}
type service struct {
	db      *database.DB
	now     func() time.Time
	mirror  func(context.Context) (string, error)
	newest  func(context.Context, string, imageline.Scope, imageline.Access) (imageline.Answer, error)
	flights singleflight.Group
}

func New(db *database.DB, now func() time.Time, mirror func(context.Context) (string, error)) *service {
	return &service{db: db, now: clock.Or(now), mirror: mirror, newest: imageline.Newest}
}
func (s *service) Answer(ctx context.Context, ref string, scope imageline.Scope) (Reading, error) {
	line, err := imageline.Parse(ref, scope)
	if err != nil {
		return Reading{}, err
	}
	result := s.flights.DoChan(line.Key(), func() (any, error) {
		db := q.New(s.db.SQL())
		old, err := db.Reading(ctx, line.Key())
		found := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Reading{}, err
		}
		reading := Reading{Answer: imageline.Answer{Tag: old.Tag, Digest: old.Digest, Registry: old.Registry}, ReadAt: old.ReadAt}
		age := s.now().Sub(old.ReadAt)
		if found && age < 24*time.Hour {
			return reading, nil
		}
		var mirror string
		if s.mirror != nil {
			mirror, err = s.mirror(ctx)
		}
		var answer imageline.Answer
		if err == nil || errors.Is(err, sql.ErrNoRows) {
			answer, err = s.newest(ctx, ref, scope, imageline.Access{Mirror: mirror, Transport: trust.Lazy(trust.Deployments)})
		}
		if err != nil {
			if found && age < 7*24*time.Hour {
				reading.Stale = true
				reading.Error = err.Error()
				return reading, nil
			}
			return Reading{}, fmt.Errorf("resolving %s: %w", ref, err)
		}
		reading = Reading{Answer: answer, ReadAt: s.now().UTC()}
		err = db.Record(ctx, q.RecordParams{ID: ids.New(reading.ReadAt), Line: line.Key(), Tag: answer.Tag, Digest: answer.Digest, Registry: answer.Registry, ReadAt: reading.ReadAt})
		return reading, err
	})
	select {
	case <-ctx.Done():
		return Reading{}, ctx.Err()
	case r := <-result:
		if r.Err != nil {
			return Reading{}, r.Err
		}
		return r.Val.(Reading), nil
	}
}
