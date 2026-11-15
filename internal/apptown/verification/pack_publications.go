package verification

import (
	"context"
	"errors"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

const PacksCatchUpJob = "packs-catch-up"

// RunPacksCatchUp reassesses behind projects under the current pack rule.
func (f Fleet) RunPacksCatchUp(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
	return nil, f.db.InTx(ctx, func(tx *database.Tx) error {
		if f.standing == nil {
			return nil
		}
		rule, err := f.standing.PackRuleIn(ctx, tx)
		if err != nil || rule.RuleID.IsZero() {
			return err
		}
		return f.startPackAssessmentsIn(ctx, tx, rule, "", ids.Nil, nil)
	})
}

// CatchUpProject starts the nightly assessment for just this project. A zero
// ID means the project is current. Schedules records the immediate requester.
func (f Fleet) CatchUpProject(ctx context.Context, project ids.ID) (ids.ID, error) {
	if project.IsZero() {
		return ids.Nil, fault.New(fault.Invalid, "name the project to catch up")
	}
	var started ids.ID
	if f.standing == nil {
		return ids.Nil, fault.New(fault.Unprocessable, "waits for a standing approval")
	}
	err := f.db.InTx(ctx, func(tx *database.Tx) error {
		rule, err := f.standing.PackRuleIn(ctx, tx)
		if err != nil {
			return err
		}
		if rule.RuleID.IsZero() {
			return fault.New(fault.Unprocessable, "waits for a standing approval")
		}
		return f.startPackAssessmentsIn(ctx, tx, rule, "", project, &started)
	})
	if err != nil {
		return ids.Nil, err
	}
	return started, nil
}

// PublishedPackIn starts an assessment under current human consent, atomically
// with publication. The assessment reports impact even for ineligible projects.
func (f Fleet) PublishedPackIn(ctx context.Context, tx *database.Tx, pack string, version int64) error {
	if f.standing == nil {
		return nil
	}
	rule, err := f.standing.PackRuleIn(ctx, tx)
	if err != nil || rule.RuleID.IsZero() {
		return err
	}
	return f.startPackAssessmentsIn(ctx, tx, rule, pack, ids.Nil, nil)
}

// PackRuleEnabledIn catches up each project once, including projects with earlier
// finished assessments: assessment never proves adoption of its candidate.
func (f Fleet) PackRuleEnabledIn(ctx context.Context, tx *database.Tx, rule workflow.StandingRevision) error {
	return f.startPackAssessmentsIn(ctx, tx, rule, "", ids.Nil, nil)
}

func (f Fleet) startPackAssessmentsIn(ctx context.Context, tx *database.Tx, rule workflow.StandingRevision, pack string, only ids.ID, started *ids.ID) error {
	publications, err := f.publications.LatestPackPublicationsIn(ctx, tx)
	if err != nil {
		return err
	}
	var projects []adoption.ProjectRef
	seen := make(map[ids.ID]bool)
	for _, publication := range publications {
		if pack != "" && publication.Name != pack {
			continue
		}
		below, err := adoption.ProjectsBelowIn(ctx, tx, publication.Name, publication.Version)
		if err != nil {
			return err
		}
		for _, p := range below {
			if (only.IsZero() || p.ID == only) && !seen[p.ID] {
				projects = append(projects, p)
				seen[p.ID] = true
			}
		}
	}
	active, err := activeOnly(ctx, tx, projects)
	if err != nil || len(active) == 0 {
		return err
	}
	ctx = access.WithCaller(ctx, access.Caller{Verified: true, Receipt: "apptown-system", Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "schedule"}})
	ctx = conformance.WithPackReview(ctx, f.standing.PackReviewAuthorityIn(tx, rule))
	for _, project := range active {
		members, err := adoption.ProjectPacksIn(ctx, tx, project.ID)
		if err != nil {
			return err
		}
		var outdated []conformance.PackVersion
		platform := false
		for _, publication := range publications {
			held := false
			for _, member := range members {
				if member.Pack == publication.Name {
					held = true
					if member.Version < publication.Version {
						outdated = append(outdated, conformance.PackVersion{Pack: publication.Name, Version: publication.Version})
					}
				}
			}
			if publication.Name == adoption.PlatformPack {
				platform = true
				// Every project with a repository is on the platform pack: one
				// that holds none of it is below this publication
				// (adoption.ProjectsBelowIn selected it for that).
				if !held {
					outdated = append(outdated, conformance.PackVersion{Pack: publication.Name, Version: publication.Version})
				}
			}
		}
		if len(outdated) == 0 {
			continue
		}
		if !platform {
			return errors.New("the platform pack publication is unavailable")
		}
		in := conformance.WhatIfRequest{Projects: []ids.ID{project.ID}, Pack: outdated[0].Pack, Version: outdated[0].Version, Also: outdated[1:], RuleID: rule.RuleID, RuleRevision: rule.Revision}
		// Keep the adoption procedure available even on old language packs.
		if in.Pack != adoption.PlatformPack {
			included := false
			for _, other := range in.Also {
				included = included || other.Pack == adoption.PlatformPack
			}
			if !included {
				for _, publication := range publications {
					if publication.Name == adoption.PlatformPack {
						in.Also = append(in.Also, conformance.PackVersion{Pack: publication.Name, Version: publication.Version})
					}
				}
			}
		}
		id, err := f.StartWhatIfIn(ctx, tx, in)
		if err != nil {
			return err
		}
		if started != nil {
			*started = id
		}
	}
	return nil
}

// PackPublication is the publication feed's current version of one pack.
type PackPublication struct {
	Name    string
	Version int64
}
type packPublications interface {
	LatestPackPublicationsIn(context.Context, database.Queryer) ([]PackPublication, error)
}
