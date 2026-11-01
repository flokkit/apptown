package deployment

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/ids"
)

const DetailObservationJob = "deployment-detail-observation"

// DetailReading dates a stored page reading separately from its last attempt.
// A failed collection keeps the last good value until it expires.
type DetailReading struct {
	ObservedAt  time.Time `json:"observedAt,omitzero"`
	AttemptedAt time.Time `json:"attemptedAt,omitzero"`
	Failure     string    `json:"failure,omitempty"`
	Stale       bool      `json:"stale"`
	Unknown     bool      `json:"unknown"`
}

type detailSnapshot[T any] struct {
	Reading DetailReading
	Value   T
}

// detailIdentity uses only local records. Settings changes and replacements
// cannot inherit a reading of another configuration or generation.
func (s *Service) detailIdentity(ctx context.Context, dep *Deployment) (string, error) {
	target, err := s.Targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return "", err
	}
	chosen, err := s.env.secrets.Profiles.Chosen(ctx, dep.ProjectID, dep.Name)
	if err != nil {
		return "", err
	}
	overrides, err := s.Overrides(ctx, dep.ID)
	if err != nil {
		return "", err
	}
	owned, err := s.Deployments.Owned(ctx, dep.ID)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(struct {
		Deployment any
		Revision   int64
		Chosen     string
		Overrides  map[string]Override
		Owned      any
	}{dep.ReadingBasis(), target.SettingsRevision, chosen, overrides, owned})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

func loadDeploymentDetail[T any](ctx context.Context, s *Service, dep *Deployment, identity, kind string) (detailSnapshot[T], error) {
	var out detailSnapshot[T]
	r, err := deploymentsql.New(s.db.SQL()).Reading(ctx, deploymentsql.ReadingParams{Subject: dep.ID.String(), Kind: "detail-" + kind})
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(r.Payload, &out)
	if r.Identity != identity {
		var empty T
		out.Value = empty
		out.Reading.Unknown = true
		out.Reading.Stale = true
		out.Reading.Failure = "The deployment changed. Waiting for a background check."
	}
	return out, err
}

func readDeploymentDetail[T any](ctx context.Context, s *Service, project ids.ID, name, kind string) (T, error) {
	var empty T
	dep, err := s.Deployments.ByName(ctx, project, name)
	if err != nil {
		return empty, err
	}
	identity, err := s.detailIdentity(ctx, dep)
	if err != nil {
		return empty, err
	}
	stored, err := loadDeploymentDetail[T](ctx, s, dep, identity, kind)
	if err != nil {
		return empty, err
	}
	reading := stored.Reading
	reading.Stale = reading.Stale || reading.ObservedAt.IsZero() || s.now().Sub(reading.ObservedAt) > 15*time.Minute
	reading.Unknown = reading.Unknown || reading.ObservedAt.IsZero() || s.now().Sub(reading.ObservedAt) > 6*time.Hour
	if reading.Unknown && reading.Failure == "" {
		reading.Failure = "The last background check is more than six hours old."
	}
	if reading.AttemptedAt.IsZero() {
		reading.Failure = "Waiting for the first background check."
	}

	switch v := any(&stored.Value).(type) {
	case *Diagnostics:
		v.Reading = reading
		if reading.Unknown {
			v.Deployment = dep.Name
			v.Generation = dep.ObservedGeneration
			v.Desired = dep.DesiredGeneration
			for i := range v.Parts {
				v.Parts[i].Presence = presenceUnknown
				v.Parts[i].Stale = true
				v.Parts[i].Reason = reading.Failure
			}
			if len(v.Parts) == 0 {
				v.Parts = []PartDiagnosis{}
				v.Unsupported = reading.Failure
			}
		}
	case *DeploymentResources:
		v.Reading = reading
		if reading.Unknown || reading.Failure != "" {
			for i := range v.Parts {
				v.Parts[i].Observed.Known = false
				v.Parts[i].Observed.Reason = reading.Failure
			}
		}
	case *[]AIModelsKey:
		for i := range *v {
			(*v)[i].Reading = reading
			if reading.Failure != "" {
				(*v)[i].Unknown = reading.Failure
			}
			if reading.Unknown {
				(*v)[i].Allowance = nil
			}
		}
	}
	return stored.Value, nil
}

func collectDeploymentDetail[T any](ctx context.Context, s *Service, dep *Deployment, identity, kind string, collect func(context.Context, ids.ID, string) (T, error), failure func(T) string) error {
	prior, err := loadDeploymentDetail[T](ctx, s, dep, identity, kind)
	if err != nil {
		return err
	}
	value, checkErr := collect(ctx, dep.ProjectID, dep.Name)
	reason := ""
	if checkErr != nil {
		reason = "The background check failed: " + safeReason(checkErr)
	} else {
		reason = failure(value)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	if ctx.Err() != nil {
		checkErr = ctx.Err()
		reason = "The background check timed out or was cancelled."
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	current, err := s.Deployments.ByID(finish, dep.ID)
	if err != nil {
		return err
	}
	currentIdentity, err := s.detailIdentity(finish, current)
	if err != nil || currentIdentity != identity {
		return err
	}
	if d, ok := any(&value).(*Diagnostics); ok && checkErr == nil {
		last := any(prior.Value).(Diagnostics)
		for i := range d.Parts {
			p := &d.Parts[i]
			if p.Presence != presenceUnknown {
				continue
			}
			for _, old := range last.Parts {
				if old.Part == p.Part {
					p.Facts, p.ObservedAt, p.Generation = old.Facts, old.ObservedAt, old.Generation
					break
				}
			}
		}
		prior.Value = value
	}
	prior.Reading.AttemptedAt = s.now()
	prior.Reading.Failure = reason
	if prior.Reading.ObservedAt.IsZero() {
		prior.Value = value
	}
	if reason == "" {
		prior.Value = value
		prior.Reading.Unknown, prior.Reading.Stale = false, false
		prior.Reading.ObservedAt = s.now()
	}
	raw, err := json.Marshal(prior)
	if err != nil {
		return err
	}
	err = deploymentsql.New(s.db.SQL()).StoreReading(finish, deploymentsql.StoreReadingParams{ID: ids.New(s.now()), Subject: dep.ID.String(), Kind: "detail-" + kind, Identity: identity, Payload: raw})
	return errors.Join(checkErr, err)
}

// observeDetails collects the three page readings independently. A failed host
// sample cannot erase a good allowance or the previous diagnostics.
func (s *Service) observeDetails(ctx context.Context, id ids.ID) error {
	dep, err := s.Deployments.ByID(ctx, id)
	if err != nil {
		return err
	}
	identity, err := s.detailIdentity(ctx, dep)
	if err != nil {
		return err
	}
	var errs []error
	errs = append(errs, collectDeploymentDetail(ctx, s, dep, identity, "Diagnostics", s.collectDiagnostics, func(d Diagnostics) string {
		if !d.Containers {
			return ""
		}
		for _, p := range d.Parts {
			if p.Presence == presenceUnknown {
				return p.Reason
			}
		}
		return ""
	}))
	errs = append(errs, collectDeploymentDetail(ctx, s, dep, identity, "Resources", s.collectResources, func(r DeploymentResources) string {
		if dep.Container == "" {
			return ""
		}
		for _, p := range r.Parts {
			if !p.Observed.Known {
				return p.Observed.Reason
			}
		}
		return ""
	}))
	errs = append(errs, collectDeploymentDetail(ctx, s, dep, identity, "AIModels", s.collectAIModels, func(keys []AIModelsKey) string {
		for _, k := range keys {
			if k.Unknown != "" {
				return k.Unknown
			}
		}
		return ""
	}))
	return errors.Join(errs...)
}

func (s *Service) RunDetailObservation(ctx context.Context, key string, _ map[string]any) (map[string]any, error) {
	raw, _, _ := strings.Cut(key, "/")
	id, err := ids.Parse(raw)
	if err != nil {
		return nil, jobs.Permanent(err)
	}
	parts := strings.Split(key, "/")
	if len(parts) == 4 {
		if _, err := ids.Parse(parts[3]); err != nil {
			return nil, jobs.Permanent(err)
		}
		key = strings.Join(parts[:3], "/")
	}
	current, err := s.DetailObservationKey(ctx, id)
	if err != nil {
		return nil, err
	}
	if current != key {
		return nil, nil
	}
	return nil, s.observeDetails(ctx, id)
}

// DetailObservationKey binds admission to a local configuration revision.
func (s *Service) DetailObservationKey(ctx context.Context, id ids.ID) (string, error) {
	dep, err := s.Deployments.ByID(ctx, id)
	if err != nil {
		return "", err
	}
	identity, err := s.detailIdentity(ctx, dep)
	return dep.ID.String() + "/" + strconv.FormatInt(dep.ObservedGeneration, 10) + "/" + identity, err
}
