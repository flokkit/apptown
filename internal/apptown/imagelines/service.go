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
	Cached(context.Context, string, imageline.Scope) (Reading, bool, error)
	Answer(context.Context, string, imageline.Scope) (Reading, error)
	Digest(context.Context, string) (string, error)
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

// Digest proves a candidate tag through an uncached manifest HEAD, using the
// same mirror and deployments trust as newest readings.
func (s *service) Digest(ctx context.Context, ref string) (string, error) {
	var mirror string
	var err error
	if s.mirror != nil {
		mirror, err = s.mirror(ctx)
	}
	if err != nil {
		return "", err
	}
	return imageline.Digest(ctx, ref, imageline.Access{Mirror: mirror, Transport: trust.Lazy(trust.Deployments)})
}

// Cached reads the stored answer without contacting a registry. Missing or expired is unknown.
func (s *service) Cached(ctx context.Context, ref string, scope imageline.Scope) (Reading, bool, error) {
	line, err := imageline.Parse(ref, scope)
	if err != nil {
		return Reading{}, false, err
	}
	row, err := q.New(s.db.SQL()).Reading(ctx, line.Key())
	if errors.Is(err, sql.ErrNoRows) {
		return Reading{}, false, nil
	}
	if err != nil {
		return Reading{}, false, err
	}
	age := s.now().Sub(row.ReadAt)
	return Reading{Answer: imageline.Answer{Tag: row.Tag, Digest: row.Digest, Registry: row.Registry}, ReadAt: row.ReadAt, Stale: age >= 24*time.Hour}, age < 7*24*time.Hour, nil
}
