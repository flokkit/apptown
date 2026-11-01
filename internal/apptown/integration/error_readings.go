package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/apptown/integration/internal/integrationsql"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/ids"
)

const ErrorObservationJob = "error-report-observation"

type errorReading struct {
	ObservedAt  time.Time           `json:"observedAt,omitzero"`
	AttemptedAt time.Time           `json:"attemptedAt,omitzero"`
	Failure     string              `json:"failure,omitempty"`
	Issues      []map[string]string `json:"issues"`
}

func (s *Service) errorIdentity(ctx context.Context, p *project.Project) (string, string, error) {
	binding, resource, err := s.Attachments.Attached(ctx, p.ID, CapErrors)
	if err != nil {
		return "", "", err
	}
	b, err := s.Bindings.ByName(ctx, binding)
	if err != nil {
		return "", "", err
	}
	raw, err := json.Marshal(struct {
		Binding        *Binding
		Resource, Slug string
	}{b, resource, p.Slug})
	if err != nil {
		return "", "", err
	}
	return binding, fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}
func (s *Service) loadErrorReading(ctx context.Context, id ids.ID, identity string) (errorReading, error) {
	var out errorReading
	row, err := integrationsql.New(s.Bindings.db.SQL()).ResourceReading(ctx, integrationsql.ResourceReadingParams{ProjectID: id, Capability: CapErrors})
	if errors.Is(err, sql.ErrNoRows) || err == nil && row.Identity != identity {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(row.Payload, &out)
	return out, err
}

// ErrorsIssueSummary enriches the page from the background job's saved reading.
func (s *Service) ErrorsIssueSummary(ctx context.Context, p *project.Project, resource map[string]any) error {
	_, identity, err := s.errorIdentity(ctx, p)
	if err != nil {
		return err
	}
	r, err := s.loadErrorReading(ctx, p.ID, identity)
	if err != nil {
		return err
	}
	resource["issuesObservedAt"] = r.ObservedAt
	resource["issuesAttemptedAt"] = r.AttemptedAt
	resource["issuesStale"] = r.ObservedAt.IsZero() || s.now().Sub(r.ObservedAt) > 15*time.Minute
	if r.ObservedAt.IsZero() {
		resource["issuesError"] = "Waiting for the first background error-reporting check."
	} else if s.now().Sub(r.ObservedAt) > 6*time.Hour {
		resource["issuesError"] = "The last error-reporting check is more than six hours old."
	} else {
		resource["unresolved"] = r.Issues
	}
	if r.Failure != "" {
		resource["issuesError"] = r.Failure
	}
	return nil
}

func (s *Service) observeErrorReport(ctx context.Context, p *project.Project) error {
	binding, identity, err := s.errorIdentity(ctx, p)
	if err != nil {
		return err
	}
	prior, err := s.loadErrorReading(ctx, p.ID, identity)
	if err != nil {
		return err
	}
	resource := map[string]any{}
	s.collectErrorsIssueSummary(ctx, p, resource, binding)
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	prior.AttemptedAt = s.now()
	prior.Failure = ""
	if why, ok := resource["issuesError"].(string); ok {
		prior.Failure = "The error-reporting check failed: " + why
	} else {
		issues, ok := resource["unresolved"].([]map[string]string)
		if !ok {
			prior.Failure = "The error-reporting service did not return an issue summary. Check its credentials."
		} else {
			prior.Issues = issues
			prior.ObservedAt = s.now()
		}
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	_, current, err := s.errorIdentity(finish, p)
	if err != nil || current != identity {
		return err
	}
	raw, err := json.Marshal(prior)
	if err != nil {
		return err
	}
	return integrationsql.New(s.Bindings.db.SQL()).StoreResourceReading(finish, integrationsql.StoreResourceReadingParams{ID: ids.New(s.now()), ProjectID: p.ID, Capability: CapErrors, Identity: identity, Payload: raw})
}

func (s *Service) ObserveErrorReports(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
	subjects, err := integrationsql.New(s.Bindings.db.SQL()).ErrorReportSubjects(ctx)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, id := range subjects {
		p, err := project.ByIDIn(ctx, s.Bindings.db, id)
		if err == nil {
			err = s.observeErrorReport(ctx, &p)
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}
