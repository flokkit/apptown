package adoption

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/wow"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption/internal/adoptionsql"
	"flokkit.io/forge/internal/stamp"
)

// ProjectPackRecord is a project's membership of a pack at a version,
// with that version's spec (empty when the version row is missing).
type ProjectPackRecord struct {
	Pack    string
	Version int64
	// Recommended is the pack's recommended version: the update a
	// project on another version is offered.
	Recommended int64
	Reason      string
	AdoptedBy   string
	// AdoptedByPrincipal is the adopter's principal; zero for AppTown, or
	// one not resolved to a person.
	AdoptedByPrincipal ids.ID
	AdoptedAt          stamp.Time
	SpecJSON           string
}

// ProjectPacks reads a project's memberships with each selected version's
// contents through q. A missing version row keeps its membership with no
// known contents.
func ProjectPacksIn(ctx context.Context, q database.Queryer, projectID ids.ID) ([]ProjectPackRecord, error) {
	rows, err := adoptionsql.New(q.SQL()).ProjectPacks(ctx, projectID)
	var out []ProjectPackRecord
	for _, r := range rows {
		p, pp := r.Pack, r.ProjectPack
		out = append(out, ProjectPackRecord{Pack: p.Name, Version: pp.Version, Recommended: p.RecommendedVersion, Reason: pp.Reason, AdoptedBy: pp.AdoptedBy, AdoptedByPrincipal: pp.AdoptedByPrincipal, AdoptedAt: stamp.Time{NullTime: sql.NullTime{Time: pp.AdoptedAt, Valid: true}}, SpecJSON: string(r.VersionSpec)})
	}
	return out, err
}

// PicksWith is the project's current pack picks with pack at version,
// added when the project is not on it: the one change a what-if describes
// and a pack-version eval's variant adopts (design/subsystems/conformance.md),
// with each also pack the project already holds at its version. Everything
// else the project picked stays as it is, in its order.
func (s *Service) PicksWith(ctx context.Context, projectID ids.ID, pack string, version int64, also ...PackPick) ([]PackPick, error) {
	picks, err := s.currentPicks(ctx, projectID)
	if err != nil {
		return nil, err
	}
	// Each other pack moves only where the project already holds it: a
	// what-if moving a fleet's own packs with the platform pack never adds
	// one project's pack to another.
	for _, a := range also {
		for i := range picks {
			if picks[i].Pack == a.Pack {
				picks[i].Version = a.Version
			}
		}
	}
	for i := range picks {
		if picks[i].Pack == pack {
			picks[i].Version = version
			return picks, nil
		}
	}
	return append(picks, PackPick{Pack: pack, Version: version}), nil
}

// currentPicks is the project's pack memberships at their versions, the
// pack its receipt names first: the first pick is the one a candidate's
// receipt names, so the order is part of what is adopted. A project with
// no adoption recorded yet has no first pack.
func (s *Service) currentPicks(ctx context.Context, projectID ids.ID) ([]PackPick, error) {
	return currentPicksIn(ctx, s.db, projectID)
}

func currentPicksIn(ctx context.Context, q database.Queryer, projectID ids.ID) ([]PackPick, error) {
	primary := ""
	current, err := CurrentReceiptIn(ctx, q, projectID)
	if found, err := database.Lookup(err); err != nil {
		return nil, fmt.Errorf("reading the project's adoption: %w", err)
	} else if found {
		primary = current.Receipt.Pack
	}
	memberships, err := ProjectPacksIn(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	picks := []PackPick{}
	for _, m := range memberships {
		pick := PackPick{Pack: m.Pack, Version: m.Version}
		if m.Pack == primary {
			picks = append([]PackPick{pick}, picks...)
		} else {
			picks = append(picks, pick)
		}
	}
	return picks, nil
}

// Membership is a project's pick of a pack at a version.
type Membership struct {
	Pack    string
	Version int64
}

// SetMembershipIn replaces a project's pack memberships inside the
// caller's transaction, at the caller's clock; an unknown pack name is an
// error.
func SetMembershipIn(ctx context.Context, tx *database.Tx, projectID ids.ID, picks []Membership, reason, who string, at time.Time) error {
	q := adoptionsql.New(tx.SQL())
	if _, err := q.DeleteProjectPacks(ctx, projectID); err != nil {
		return err
	}
	adopter, err := identity.ActorIn(ctx, tx, who)
	if err != nil {
		return err
	}
	for _, pk := range picks {
		p, err := wow.PackByNameIn(ctx, tx, pk.Pack)
		if err != nil {
			return err
		}
		if _, err := q.InsertProjectPack(ctx, adoptionsql.InsertProjectPackParams{ID: ids.New(at.UTC()), ProjectID: projectID, PackID: p.ID, Version: pk.Version, Reason: reason, Actor: who, ActorPrincipal: adopter.Principal, ActorVia: adopter.Via, At: at.UTC()}); err != nil {
			return err
		}
	}
	return nil
}

func canonJSON(s string) string {
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// ProjectRef names a project.
type ProjectRef struct {
	ID   ids.ID
	Slug string
}

// ProjectsBelowIn is every ordinary project with a repository that directly
// holds pack at a version below version, by slug: who a new version is tried
// on by default. A project on that version has nothing to try; the default
// is upgrade-only, so one on a later version is left out; a project on a
// pack that includes this one takes it through that pack's own version.
// Whether a project is active is its owner's answer, asked by the caller.
// It checks no authority: the caller holds its own (a what-if's admission).
func ProjectsBelowIn(ctx context.Context, q database.Queryer, pack string, version int64) ([]ProjectRef, error) {
	rows, err := adoptionsql.New(q.SQL()).ProjectsOnPackBelow(ctx, adoptionsql.ProjectsOnPackBelowParams{Pack: pack, Version: version})
	if err != nil {
		return nil, err
	}
	out := make([]ProjectRef, 0, len(rows))
	for _, r := range rows {
		out = append(out, ProjectRef{ID: r.ID, Slug: r.Slug})
	}
	return out, nil
}

// ProjectsBelow is ProjectsBelowIn limited to the projects the caller may
// read.
func (s *Service) ProjectsBelow(ctx context.Context, pack string, version int64) ([]ProjectRef, error) {
	if s.deps.Authority == nil {
		return nil, access.ErrAuthorityUnavailable
	}
	visible, err := s.deps.Authority.VisibleProjects(ctx, access.CapProjectRead)
	if err != nil {
		return nil, err
	}
	all, err := ProjectsBelowIn(ctx, s.db, pack, version)
	if err != nil {
		return nil, err
	}
	out := []ProjectRef{}
	for _, r := range all {
		if slices.Contains(visible, r.ID) {
			out = append(out, r)
		}
	}
	return out, nil
}

// replacePulledPrimaryIn changes only the explicitly pulled primary membership.
// Supplemental membership rows and their original attribution remain intact.
func replacePulledPrimaryIn(ctx context.Context, tx *database.Tx, projectID ids.ID, selected wow.Pack, who string, at time.Time) error {
	current, err := CurrentIn(ctx, tx, projectID)
	if err != nil {
		return err
	}
	previous := current.Pack
	if previous != "" {
		old, err := wow.PackByNameIn(ctx, tx, previous)
		if err != nil {
			return err
		}
		// Every repository keeps its platform membership.
		if old.Platform() {
			previous = ""
		}
	}
	adopter, err := identity.ActorIn(ctx, tx, who)
	if err != nil {
		return err
	}
	q := adoptionsql.New(tx.SQL())
	if err := q.DeletePulledPrimaryMembership(ctx, adoptionsql.DeletePulledPrimaryMembershipParams{ProjectID: projectID, PreviousPack: previous, SelectedPack: selected.Name}); err != nil {
		return err
	}
	_, err = q.InsertProjectPack(ctx, adoptionsql.InsertProjectPackParams{ID: ids.New(at.UTC()), ProjectID: projectID, PackID: selected.ID, Version: selected.LatestVersion, Reason: "pack " + selected.Name + " pulled (lanes)", Actor: who, ActorPrincipal: adopter.Principal, ActorVia: adopter.Via, At: at.UTC()})
	return err
}
