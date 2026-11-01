package preview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/apptown/preview/internal/previewsql"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

// Subject is local work identity selected by Work for background collection.
type Subject struct {
	ID, ProjectID ids.ID
	InProgress    bool
}

const ObservationJob = "preview-observation"

type Reading struct {
	ObservedAt  time.Time `json:"observedAt,omitzero"`
	AttemptedAt time.Time `json:"attemptedAt,omitzero"`
	Failure     string    `json:"failure,omitempty"`
	Stale       bool      `json:"stale"`
	Unknown     bool      `json:"unknown"`
}
type previewReading struct {
	Absent bool `json:"-"`
	Reading
	Live  []LiveChange
	Value observation
}

func (s *Service) loadReading(ctx context.Context, subject string) (previewReading, error) {
	out := previewReading{}
	row, err := previewsql.New(s.db.SQL()).Reading(ctx, subject)
	if errors.Is(err, sql.ErrNoRows) {
		out.Absent = true
		return out, nil
	}
	if err == nil && row.Identity != s.observationIdentity {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(row.Payload, &out)
	return out, err
}
func (s *Service) readingAt(r Reading) Reading {
	r.Stale = r.ObservedAt.IsZero() || s.now().Sub(r.ObservedAt) > 15*time.Minute
	r.Unknown = r.ObservedAt.IsZero() || s.now().Sub(r.ObservedAt) > 6*time.Hour
	if r.AttemptedAt.IsZero() {
		r.Failure = "Waiting for the first background preview check."
	}
	return r
}

// ObservePreviews collects status for local work identities chosen by Work.
// Every provider call is here; pages read the durable snapshot only.
func (s *Service) ObservePreviews(ctx context.Context, subjects []Subject, started time.Time) error {
	live := []LiveChange{}
	keep := []string{"instance"}
	var errs []error
	for _, subject := range subjects {
		keep = append(keep, subject.ID.String())
		r, err := s.observeSubject(ctx, subject)
		if err != nil {
			errs = append(errs, err)
		}
		if r.Value.URL != "" {
			live = append(live, LiveChange{ChangeID: subject.ID, StartedAt: r.Value.StartedAt})
		}
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	r, err := s.loadReading(finish, "instance")
	if err != nil {
		return errors.Join(errors.Join(errs...), err)
	}
	r.AttemptedAt = s.now()
	if len(errs) > 0 || ctx.Err() != nil {
		r.Failure = "The preview check failed."
	} else {
		r.ObservedAt = s.now()
		r.Live = live
		r.Failure = ""
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := previewsql.New(s.db.SQL()).StoreReading(finish, previewsql.StoreReadingParams{ID: ids.New(s.now()), Subject: "instance", Identity: s.observationIdentity, Payload: raw}); err != nil {
		return err
	}
	if len(errs) == 0 && ctx.Err() == nil {
		if err := previewsql.New(s.db.SQL()).PruneReadings(finish, previewsql.PruneReadingsParams{Keep: keep, Before: started}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ListedChangeIDs limits selection to open work and previews the runtime retains.
func (s *Service) ListedChangeIDs(ctx context.Context) ([]ids.ID, error) {
	var names []string
	var err error
	if s.worker != nil {
		names, err = s.worker.List(ctx)
	} else if s.hosts.Local().Absent() {
		err = docker.ErrNoLocalBackend
	} else {
		names, err = s.hosts.Local().List(ctx, "ps", "-a", "--format", "{{.Names}}")
	}
	var out []ids.ID
	for _, name := range names {
		if id, ok := docker.ParsePreviewChangeID(name); ok {
			out = append(out, id)
		}
	}
	if err != nil && !errors.Is(ctx.Err(), context.Canceled) {
		prior, loadErr := s.loadReading(ctx, "instance")
		if loadErr != nil {
			return nil, errors.Join(err, loadErr)
		}
		prior.AttemptedAt = s.now()
		prior.Failure = "The preview check failed: " + err.Error()
		raw, marshalErr := json.Marshal(prior)
		if marshalErr != nil {
			return nil, errors.Join(err, marshalErr)
		}
		finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		storeErr := previewsql.New(s.db.SQL()).StoreReading(finish, previewsql.StoreReadingParams{ID: ids.New(s.now()), Subject: "instance", Identity: s.observationIdentity, Payload: raw})
		updateErr := previewsql.New(s.db.SQL()).FailReadings(finish, previewsql.FailReadingsParams{At: s.now(), Failure: prior.Failure, Identity: s.observationIdentity})
		err = errors.Join(err, storeErr, updateErr)
	}
	return out, err
}

// ObserveChange uses the same collector for a post-act and a scheduled check.
func (s *Service) ObserveChange(ctx context.Context, subject Subject) error {
	_, err := s.observeSubject(ctx, subject)
	return err
}
func (s *Service) observeSubject(ctx context.Context, subject Subject) (previewReading, error) {
	lock, _ := s.observing.LoadOrStore(subject.ID, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	prior, err := s.loadReading(ctx, subject.ID.String())
	if err != nil {
		return prior, err
	}
	o := s.observeChange(ctx, subject.ProjectID, subject.ID, subject.InProgress)
	if errors.Is(ctx.Err(), context.Canceled) {
		return prior, ctx.Err()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		o.Unavailable = "The preview check timed out."
	}
	prior.AttemptedAt = s.now()
	prior.Failure = o.Unavailable
	if o.Unavailable == "" {
		prior.Value = o
		prior.ObservedAt = s.now()
	}
	raw, err := json.Marshal(prior)
	if err != nil {
		return prior, err
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	err = previewsql.New(s.db.SQL()).StoreReading(finish, previewsql.StoreReadingParams{ID: ids.New(s.now()), Subject: subject.ID.String(), Identity: s.observationIdentity, Payload: raw})
	if prior.Failure != "" {
		err = errors.Join(err, errors.New(prior.Failure))
	}
	return prior, err
}

func (s *Service) recordedChangeStatus(ctx context.Context, id ids.ID) Status {
	// An admitted boot is local operation state, and takes precedence over readings.
	current := s.runtime.status(changeKey(id), observation{})
	if current.State == stateStarting {
		return current
	}

	r, err := s.loadReading(ctx, id.String())
	reading := s.readingAt(r.Reading)
	if (current.State == Broken || current.State == unavailable) && !reading.ObservedAt.After(current.At) {
		current.Reading = reading
		return current
	}
	if err != nil {
		reading.Unknown = true
		reading.Failure = "The stored preview check could not be read."
	}
	if current.State == cold && current.At.After(r.AttemptedAt) {
		current.State = stateStarting
		current.Reading = reading
		return current
	}
	if r.Absent && err == nil {
		return Status{State: cold, Reading: reading}
	}
	o := r.Value
	o.Known = !reading.Unknown && reading.Failure == ""
	if reading.Unknown || reading.Failure != "" {
		o = observation{Unavailable: reading.Failure}
		if o.Unavailable == "" {
			o.Unavailable = "The last preview check is more than six hours old."
		}
	}
	out := s.runtime.inspect(ctx, changeKey(id), storedObservation(o))
	out.Reading = reading
	return out
}

// LiveChangePreviews reads the last background inventory of running previews.
func (s *Service) LiveChangePreviews(ctx context.Context) ([]LiveChange, error) {
	r, err := s.loadReading(ctx, "instance")
	if err != nil {
		return nil, err
	}
	reading := s.readingAt(r.Reading)
	if reading.Failure != "" {
		return r.Live, errors.New(reading.Failure)
	}
	if reading.Unknown {
		return r.Live, errors.New("The last preview check is more than six hours old.")
	}
	return r.Live, nil
}

type storedObservation observation

func (o storedObservation) ObservePreview(context.Context) observation { return observation(o) }
