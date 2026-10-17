package forge

import (
	"context"
	"log"
)

// Adoption revisions (design/subsystems/ways-of-working-versions.md §1.1, §4.1, §7,
// §9 step 1): the exact way of working a project is on, written down as an
// immutable receipt — every guidance block at its version and position,
// every workflow at its library version, the policies, the extensions —
// with a digest. A project points at one current revision. The receipt is
// what gates, steering and workflow resolution read, so nothing that
// happens to the library later moves a project: a stock release cuts new
// versions and stops there.
//
// Slice one (the containment freeze) makes the revision exist and makes
// the paths that used to move projects silently stop: the stock sync no
// longer rides imports forward, the platform tier no longer appends to
// existing projects at boot, the pack pull is retired, workflow and skill
// resolution read the receipt. A person's own edit — guidance, lanes —
// records a new revision; that is the adoption door in its simplest form.
// The upgrade process (assess, decide, uplift) comes in later slices.

// VerifyAdoptions runs the adoption owner's drift check (VerifyRevision) on
// every project with a revision, after the boot's syncs, and names each
// project that drifted.
func (a *App) VerifyAdoptions(ctx context.Context) (drifted []string) {
	ids, err := a.Adoption.ProjectsWithRevisions(ctx)
	if err != nil {
		return nil
	}
	for _, id := range ids {
		p := a.projectByID(ctx, id)
		if p == nil {
			continue
		}
		if d := a.Adoption.VerifyRevision(ctx, p.ID); d != nil {
			log.Printf("adoption: %s drifted from r%d outside the adoption door: %s", p.Slug, d.Revision, d.Diff)
			drifted = append(drifted, p.Slug)
		}
	}
	return drifted
}
