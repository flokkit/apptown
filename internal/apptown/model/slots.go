package model

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/model/internal/modelsql"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

const StandardProfile = "standard"

// StockSlots are the purposes AppTown itself names, in page order.
var StockSlots = []string{"default", "plan", "implement", "review", "security", "fast", "chat", "worker", "scout"}

// Gloss: what each stock slot means, in ordinary words.
var Gloss = map[string]string{
	"default":   "what runs when nothing more specific is mapped; the end of every fallback chain",
	"plan":      "thinking a task through before the work starts",
	"implement": "writing the code",
	"review":    "a second set of eyes on finished work",
	"security":  "looking for ways the work could be unsafe",
	"fast":      "quick cheap jobs: summaries, briefs, small checks",
	"chat":      "the helper people talk to on a project page",
	"worker":    "a helper's delegate that carries out a written brief",
	"scout":     "a helper's delegate for quick read-only lookups",
}

// stockFallbacks: where an unmapped slot degrades to. Seeded as data so
// the chain is visible and editable; the final hop to `default` stays in
// the rule, because every chain must end somewhere.
var stockFallbacks = map[string]string{
	"plan": "default", "implement": "default", "review": "default",
	"security": "review", "fast": "default", "chat": "default",
	"worker": "default", "scout": "fast",
}

var profileNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)

var errProfileName = errors.New("profile must be a short lowercase word (letters, digits, dashes)")

// ValidProfile reports whether a profile name has the shape.
func ValidProfile(name string) bool { return profileNameRe.MatchString(name) }

// isSlot reports whether a model value names a slot rather than a
// concrete pattern.
func isSlot(v string) bool { return v != "" && !strings.Contains(v, "/") }

// profileOf is a project's declared demand level: standard when it
// declares nothing.
func profileOf(declared string) string {
	if declared == "" {
		return StandardProfile
	}
	return declared
}

// Slots stores instance mappings, project overrides and slot fallbacks.
// Callers authorize changes before invoking its mutation methods.
type Slots struct {
	db *database.DB
	// Now is AppTown's clock, which stamps a slot's change; nil is the
	// real one.
	Now func() time.Time
}

func New(db *database.DB) *Slots { return &Slots{db: db} }

func (s *Slots) now() time.Time { return clock.Or(s.Now)() }

// cell is one mapping: found is false when no row (or an empty pattern)
// maps it. A failed read is an error, never an unmapped cell.
func (s *Slots) cell(ctx context.Context, slot string, projectID ids.ID, profile string) (pattern string, found bool, err error) {
	r, err := queries(s.db).ModelSlotCell(ctx, modelsql.ModelSlotCellParams{Slot: slot, ProjectID: projectID, Profile: profile})
	if found, err := database.Lookup(err); err != nil || !found {
		return "", false, err
	}
	return r.Pattern, r.Pattern != "", nil
}

type cellRef struct {
	decided string
	project ids.ID
	profile string
}

// resolve walks, most specific first, until a cell holds a pattern:
//
//	explicit project override row (profile '')     → profile "override"
//	instance cell (slot, project's profile)        → that profile
//	instance cell (slot, standard)                 → "standard"
//	the slot's fallback (slot_fallbacks, then `default`), same lookup
//
// It returns the pattern, the slot that won (for receipts), and which
// profile column decided. A slash-pattern passes through with slot "";
// when nothing is mapped the pattern is "" with slot `default`: no model is
// set up. Empty requested selects the default slot. Cycles end the same way.
// Only an absent cell or fallback row passes the walk on: a failed read
// returns its error, so a database fault never selects a less specific model
// or reads as "no model is set up".
func (s *Slots) resolve(ctx context.Context, projectID ids.ID, projectProfile, requested string) (pattern, slot, profile string, err error) {
	if requested != "" && !isSlot(requested) {
		return requested, "", "", nil
	}
	proj := profileOf(projectProfile)
	seen := map[string]bool{}
	name := requested
	if name == "" {
		name = "default"
	}
	for name != "" && !seen[name] {
		seen[name] = true
		// Most specific first: what the receipt says decided, the cell's
		// project (Nil for the instance) and its profile column.
		var cells []cellRef
		if !projectID.IsZero() {
			cells = append(cells, cellRef{"override", projectID, ""})
		}
		cells = append(cells, cellRef{proj, ids.Nil, proj})
		if proj != StandardProfile {
			cells = append(cells, cellRef{StandardProfile, ids.Nil, StandardProfile})
		}
		for _, c := range cells {
			p, ok, err := s.cell(ctx, name, c.project, c.profile)
			if err != nil {
				return "", "", "", fmt.Errorf("reading the %s model slot: %w", name, err)
			}
			if ok {
				return p, name, c.decided, nil
			}
		}
		r, err := queries(s.db).SlotFallbackBySlot(ctx, name)
		if _, err := database.Lookup(err); err != nil {
			return "", "", "", fmt.Errorf("reading the %s model slot's fallback: %w", name, err)
		}
		if r.Fallback != "" {
			name = r.Fallback
		} else if name != "default" {
			name = "default"
		} else {
			break
		}
	}
	return "", "default", "", nil
}

// SeedFallbacks records the stock chain as data, only where no row says
// otherwise: the chain is visible and editable, and operator edits stand.
func (s *Slots) SeedFallbacks(ctx context.Context) error {
	for slot, fallback := range stockFallbacks {
		if err := queries(s.db).InsertFallback(ctx, modelsql.InsertFallbackParams{Slot: slot, Fallback: fallback}); err != nil {
			return err
		}
	}
	return nil
}

// SetDefault replaces the `default` slot's standard cell (setup).
func (s *Slots) SetDefault(ctx context.Context, pattern string) error {
	err := queries(s.db).SetDefaultSlot(ctx, modelsql.SetDefaultSlotParams{Profile: StandardProfile, Pattern: pattern, UpdatedAt: s.now().UTC()})
	return err
}

// Set maps a cell (a project override when projectID is set, else the
// instance cell for the profile) or clears it when the pattern is empty,
// with the receipt in the same transaction. A receipt failure rolls back the
// mapping. The caller must authorize the change; who is audit attribution.
// This validates the slot/profile names, not provider availability or model access.
func (s *Slots) Set(ctx context.Context, slot string, projectID ids.ID, projectSlug, profile, pattern, who string) error {
	if !isSlot(slot) {
		return errors.New("slot (no slash) required")
	}
	if projectID.IsZero() {
		if profile == "" {
			profile = StandardProfile
		}
		if !ValidProfile(profile) {
			return errProfileName
		}
	} else {
		profile = ""
	}
	at := s.now()
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		q := queries(tx)
		if pattern == "" {
			if _, err := q.DeleteSlotCell(ctx, modelsql.DeleteSlotCellParams{Slot: slot, ProjectID: projectID, Profile: profile}); err != nil {
				return err
			}
		} else if _, err := q.UpsertSlotCell(ctx, modelsql.UpsertSlotCellParams{Slot: slot, ProjectID: projectID, Profile: profile, Pattern: pattern, UpdatedAt: at.UTC()}); err != nil {
			return err
		}
		return audit.At(at).RecordIn(ctx, tx, who, projectID, "model-slot.set", slot, map[string]any{"pattern": pattern, "project": projectSlug, "profile": profile})
	})
}

// Cells is every instance cell: slot → profile → pattern.
func (s *Slots) Cells(ctx context.Context) (map[string]map[string]string, error) {
	rows, err := queries(s.db).ModelSlotsInstance(ctx)
	out := map[string]map[string]string{}
	for _, c := range rows {
		if out[c.Slot] == nil {
			out[c.Slot] = map[string]string{}
		}
		out[c.Slot][c.Profile] = c.Pattern
	}
	return out, err
}

// override is a project's explicit mapping of a slot.
type override struct{ Slot, Pattern, ProjectSlug string }

func (s *Slots) Overrides(ctx context.Context) ([]override, error) {
	rows, err := queries(s.db).Overrides(ctx)
	out := make([]override, 0, len(rows))
	for _, r := range rows {
		out = append(out, override{Slot: r.Slot, Pattern: r.Pattern, ProjectSlug: r.ProjectSlug})
	}
	return out, err
}

// Profiles is every profile in use, standard first: the instance's
// cells and the projects' declarations.
func (s *Slots) Profiles(ctx context.Context) ([]string, error) {
	rows, err := queries(s.db).Profiles(ctx)
	out := []string{StandardProfile}
	seen := map[string]bool{StandardProfile: true}
	for _, n := range rows {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, err
}

// Fallbacks is the recorded chain: slot → the slot it degrades to.
func (s *Slots) Fallbacks(ctx context.Context) (map[string]string, error) {
	rows, err := queries(s.db).Fallbacks(ctx)
	out := map[string]string{}
	for _, h := range rows {
		out[h.Slot] = h.Fallback
	}
	return out, err
}
