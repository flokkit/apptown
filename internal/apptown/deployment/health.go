package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// HealthController constructs the daemon's liveness controller. Deployment
// supplies target selection, contract intent, ingress and best-effort receipts;
// the Docker library owns probing, restart thresholds and backoff.
func (s *Service) HealthController() *docker.HealthController {
	return docker.NewHealthController(docker.HealthDependencies{
		Watch:      func(ctx context.Context) []docker.Watched { return s.hosts.Watched(ctx, s.Deployments) },
		PublicPart: s.healthPublicPart,
		FrontDoor:  s.env.FrontDoorHeaders,
		Record:     s.recordHealth,
		Now:        s.clock,
		AfterSweep: s.RecordLimitEvents,
	})
}

func (s *Service) recordHealth(ctx context.Context, w docker.Watched, kind string, payload map[string]any) {
	at := s.now()
	err := s.db.InTx(ctx, func(tx *database.Tx) error {
		if kind == "deployment.crash-loop-held" {
			payload["deploymentId"], payload["generation"] = w.DeploymentID, w.Identity.Generation
			r, err := deploymentsql.New(tx.SQL()).ReleasedGeneration(ctx, deploymentsql.ReleasedGenerationParams{ProjectID: w.ProjectID, DeploymentID: w.DeploymentID, Generation: w.Identity.Generation, State: Released.String()})
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil {
				payload["releaseId"], payload["releaseNumber"] = r.ID, r.Number
				text := fmt.Sprintf("After release: the app crash-looped (%v restarts in %v); AppTown stopped its container.", payload["restarts"], payload["window"])
				if payload["ok"] != true {
					text = fmt.Sprintf("After release: the app crash-looped (%v restarts in %v); AppTown attempted to stop its container, but the stop could not be confirmed: %v", payload["restarts"], payload["window"], payload["detail"])
				}
				if _, err := releasesql.New(tx.SQL()).AppendReleaseEvent(ctx, releasesql.AppendReleaseEventParams{ID: ids.New(at), ReleaseID: r.ID, At: at, Text: text}); err != nil {
					return err
				}
			}
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.At(at).String(), "apptown-system", w.ProjectID, kind, w.Name, string(body))
	})
	if err != nil {
		log.Printf("health controller: best-effort receipt and release note could not be recorded: %v", err)
	}
}

// healthPublicPart is the public part (with its declared health intent) of
// the release a standing deployment runs: the contract at its recorded
// release revision, not the project's newest, which may describe software
// not yet released. No recorded revision, no release sources or an
// unreadable contract is nil, and the controller uses its default probe.
// A revision's contract never changes, so it is read once per revision.
func (s *Service) healthPublicPart(ctx context.Context, w docker.Watched) *appcontract.Part {
	if s.release.Sources == nil {
		return nil
	}
	p, err := s.projects.ByID(ctx, w.ProjectID)
	if err != nil {
		return nil
	}
	dep, err := s.Deployments.ByName(ctx, p.ID, w.Name)
	if err != nil || dep.ReleaseSHA == "" {
		return nil
	}
	key := dep.ReleaseSHA
	s.healthMu.Lock()
	part, ok := s.healthParts[key]
	s.healthMu.Unlock()
	if ok {
		return part
	}
	proj, err := s.release.Sources.Project(ctx, p.ID)
	if err != nil {
		return nil
	}
	c, _, err := s.release.Sources.Contract(ctx, proj, dep.ReleaseSHA)
	if err != nil || c == nil {
		return nil // not cached: a passing read failure is asked again next tick
	}
	part = c.PublicPart()
	s.healthMu.Lock()
	if s.healthParts == nil {
		s.healthParts = map[string]*appcontract.Part{}
	}
	s.healthParts[key] = part
	s.healthMu.Unlock()
	return part
}
