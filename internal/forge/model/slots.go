package model

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/model/internal/modelsql"
)

const StandardProfile = "standard"

// StockSlots are the purposes the forge itself names, in page order.
var StockSlots = []string{"default", "plan", "implement", "review", "security", "fast", "chat", "worker", "scout", "draft"}

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
	"draft":     "the assistant that drafts workflow definitions",
}

// StockFallbacks: where an unmapped slot degrades to. Seeded as data so
// the chain is visible and editable; the final hop to `default` stays in
// the rule, because every chain must end somewhere.
var StockFallbacks = map[string]string{
	"plan": "default", "implement": "default", "review": "default",
	"security": "review", "fast": "default", "chat": "default",
	"worker": "default", "scout": "fast", "draft": "fast",
}

var profileNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)

var ErrProfileName = errors.New("profile must be a short lowercase word (letters, digits, dashes)")

// ValidProfile reports whether a profile name has the shape.
func ValidProfile(name string) bool { return profileNameRe.MatchString(name) }

// IsSlot reports whether a model value names a slot rather than a
// concrete pattern.
func IsSlot(v string) bool { return v != "" && !strings.Contains(v, "/") }

// ProfileOf is a project's declared demand level: standard when it
// declares nothing.
func ProfileOf(declared string) string {
	if declared == "" {
		return StandardProfile
	}
	return declared
}

// Slots stores instance mappings, project overrides and slot fallbacks.
// Callers authorize changes before invoking its mutation methods.
type Slots struct {
	db *database.DB
	// Now is the forge's clock, which stamps a slot's change; nil is the
	// real one.
	Now func() time.Time
}

func New(db *database.DB) *Slots { return &Slots{db: db} }

func (s *Slots) now() time.Time { return clock.Or(s.Now)() }

func (s *Slots) cell(ctx context.Context, slot string, projectID ids.ID, profile string) (string, bool) {
	r, err := queries(s.db).ModelSlotCell(ctx, modelsql.ModelSlotCellParams{Slot: slot, ProjectID: projectID, Profile: profile})
	if err != nil || r.Pattern == "" {
		return "", false
	}
	return r.Pattern, true
}

// Resolve walks, most specific first, until a cell holds a pattern:
//
//	explicit project override row (profile '')     → profile "override"
//	instance cell (slot, project's profile)        → that profile
//	instance cell (slot, standard)                 → "standard"
//	the slot's fallback (slot_fallbacks, then `default`), same lookup
//
// It returns the pattern, the slot that won (for receipts), and which
// profile column decided. A slash-pattern passes through with slot "";
// when nothing is mapped, the instance's default model wins as `default`.
// Empty requested selects the default slot. Cycles terminate at defaultModel.
// A cell read failure skips that cell; a fallback read failure ends the walk.
// The result therefore does not establish that configuration reads succeeded.
func (s *Slots) Resolve(ctx context.Context, projectID ids.ID, projectProfile, requested, defaultModel string) (pattern, slot, profile string) {
	if requested != "" && !IsSlot(requested) {
		return requested, "", ""
	}
	proj := ProfileOf(projectProfile)
	seen := map[string]bool{}
	name := requested
	if name == "" {
		name = "default"
	}
	for name != "" && !seen[name] {
		seen[name] = true
		if !projectID.IsZero() {
			if p, ok := s.cell(ctx, name, projectID, ""); ok {
				return p, name, "override"
			}
		}
		if p, ok := s.cell(ctx, name, ids.Nil, proj); ok {
			return p, name, proj
		}
		if proj != StandardProfile {
			if p, ok := s.cell(ctx, name, ids.Nil, StandardProfile); ok {
				return p, name, StandardProfile
			}
		}
		r, err := queries(s.db).SlotFallbackBySlot(ctx, name)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			break
		}
		if r.Fallback != "" {
			name = r.Fallback
		} else if name != "default" {
			name = "default"
		} else {
			break
		}
	}
	return defaultModel, "default", ""
}

// SeedDefault records the instance's default model as the `default`
// slot's standard cell, only when none is recorded: the row is the
// source of truth thereafter.
func (s *Slots) SeedDefault(ctx context.Context, defaultModel string) error {
	if defaultModel == "" {
		return nil
	}
	err := queries(s.db).InsertDefaultSlot(ctx, modelsql.InsertDefaultSlotParams{Profile: StandardProfile, Pattern: defaultModel, UpdatedAt: s.now().UTC()})
	return err
}

// SeedFallbacks records the stock chain as data, only where no row says
// otherwise: the chain is visible and editable, and operator edits stand.
func (s *Slots) SeedFallbacks(ctx context.Context) error {
	for slot, fallback := range StockFallbacks {
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
	if !IsSlot(slot) {
		return errors.New("slot (no slash) required")
	}
	if projectID.IsZero() {
		if profile == "" {
			profile = StandardProfile
		}
		if !ValidProfile(profile) {
			return ErrProfileName
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

// Override is a project's explicit mapping of a slot.
type Override struct{ Slot, Pattern, ProjectSlug string }

func (s *Slots) Overrides(ctx context.Context) ([]Override, error) {
	rows, err := queries(s.db).Overrides(ctx)
	out := make([]Override, 0, len(rows))
	for _, r := range rows {
		out = append(out, Override{Slot: r.Slot, Pattern: r.Pattern, ProjectSlug: r.ProjectSlug})
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
