package model

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/model/internal/modelsql"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

const StandardProfile = "standard"

// StockSlots are the purposes AppTown itself names, in page order.
var StockSlots = []string{"main-strong", "main-fast", "secondary-strong", "secondary-fast", "plan", "implement", "review", "checks", "security", "fast", "chat", "worker", "scout"}

// IsBase reports whether a slot is one of the four basic profile choices.
func IsBase(slot string) bool {
	switch slot {
	case "main-strong", "main-fast", "secondary-strong", "secondary-fast":
		return true
	}
	return false
}

// Gloss: what each stock slot means, in ordinary words.
var Gloss = map[string]string{
	"main-strong":      "Main · strong: the main model; the end of every fallback chain",
	"main-fast":        "Main · fast: quick everyday jobs",
	"secondary-strong": "Second opinion · strong: a second opinion on finished work",
	"secondary-fast":   "Second opinion · fast: quick independent checks",
	"plan":             "thinking a task through before the work starts",
	"implement":        "writing the code",
	"review":           "a second set of eyes on finished work",
	"checks":           "AI conformance checks",
	"security":         "looking for ways the work could be unsafe",
	"fast":             "quick jobs: approval briefs, project descriptions, setup interviews",
	"chat":             "the helper people talk to on a project page",
	"worker":           "a helper's delegate that carries out a written brief",
	"scout":            "a helper's delegate for quick read-only lookups",
}

// stockUse names a built-in job and the resolver call sites that request its slot.
// Sources are relative to internal/apptown; an empty list describes fallback use.
type stockUse struct {
	Slot    string
	Job     string
	Sources []string
}

// StockUses owns the job names shown beneath model profile tasks, beside Gloss.
// Workflow consumers are added from their published definitions.
var StockUses = []stockUse{
	{Slot: "main-strong", Job: "change summaries", Sources: []string{"narrative_wiring.go"}},
	{Slot: "main-strong", Job: "failure explanations", Sources: []string{"briefs/service.go"}},
	{Slot: "main-strong", Job: "approval briefs", Sources: []string{"briefs/approval.go"}},
	{Slot: "main-strong", Job: "tasks left unset"},
	{Slot: "main-strong", Job: "model setup status", Sources: []string{"httpapi/setup_status.go"}},
	{Slot: "chat", Job: "project chat", Sources: []string{"conversation/admission.go", "conversation/open.go", "conversation/environment.go"}},
	{Slot: "worker", Job: "helpers' written briefs", Sources: []string{"agentharness/pi/preparation.go"}},
	{Slot: "scout", Job: "helpers' quick lookups", Sources: []string{"agentharness/pi/preparation.go"}},
	{Slot: "fast", Job: "conversation request labels", Sources: []string{"narrative_wiring.go"}},
	{Slot: "fast", Job: "project descriptions", Sources: []string{"projectlifecycle/description.go"}},
	{Slot: "fast", Job: "ways-of-working setup interviews", Sources: []string{"httpapi/pack_assistance.go"}},
	{Slot: "checks", Job: "ways-of-working model checks", Sources: []string{"verification/guidance_models.go"}},
	{Slot: "checks", Job: "fleet conformance checks", Sources: []string{"verification/fleet_dependencies.go"}},
}

// stockFallbacks: where an unmapped slot degrades to. Seeded as data so
// the chain is visible and editable; the final hop to `main-strong` stays in
// the rule, because every chain must end somewhere.
var stockFallbacks = map[string]string{
	"plan": "main-strong", "implement": "main-strong", "review": "secondary-strong",
	"security": "review", "fast": "main-fast", "chat": "main-strong",
	"worker": "main-strong", "scout": "fast", "checks": "secondary-fast",
	"main-fast": "main-strong", "secondary-strong": "main-strong", "secondary-fast": "main-fast",
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

// Slots stores profile mappings and slot fallbacks.
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
func (s *Slots) cell(ctx context.Context, slot, profile string) (pattern string, found bool, err error) {
	r, err := queries(s.db).ModelSlotCell(ctx, modelsql.ModelSlotCellParams{Slot: slot, Profile: profile})
	if found, err := database.Lookup(err); err != nil || !found {
		return "", false, err
	}
	return r.Pattern, r.Pattern != "", nil
}

// resolve walks the requested slot's whole fallback chain in the project's
// profile before walking it in standard. A standard project needs only one walk.
// The winning slot and profile identify the cell for receipts. Slash patterns
// pass through. Empty requests use main-strong. Unmapped chains and cycles return
// no model with slot main-strong. Retired choices and failed reads return errors.
func (s *Slots) resolve(ctx context.Context, projectProfile, requested string) (pattern, slot, profile string, err error) {
	if err := retiredChoice(requested); err != nil {
		return "", "", "", err
	}
	if requested != "" && !isSlot(requested) {
		return requested, "", "", nil
	}
	proj := profileOf(projectProfile)
	profiles := []string{proj}
	if proj != StandardProfile {
		profiles = append(profiles, StandardProfile)
	}
	for _, profile := range profiles {
		seen := map[string]bool{}
		name := requested
		if name == "" {
			name = "main-strong"
		}
		for name != "" && !seen[name] {
			seen[name] = true
			p, ok, err := s.cell(ctx, name, profile)
			if err != nil {
				return "", "", "", fmt.Errorf("reading the %s model slot: %w", name, err)
			}
			if ok {
				return p, name, profile, nil
			}
			r, err := queries(s.db).SlotFallbackBySlot(ctx, name)
			if _, err := database.Lookup(err); err != nil {
				return "", "", "", fmt.Errorf("reading the %s model slot's fallback: %w", name, err)
			}
			if r.Fallback != "" {
				name = r.Fallback
			} else if name != "main-strong" {
				name = "main-strong"
			} else {
				break
			}
		}
	}
	return "", "main-strong", "", nil
}

// retiredChoice rejects names that no longer participate in model selection.
func retiredChoice(slot string) error {
	if slot == "default" || slot == "primary-fast" {
		return fault.New(fault.Invalid, fmt.Sprintf("unknown model choice %q: edit the model request to use main-strong or main-fast; the other basic choices are secondary-strong and secondary-fast", slot))
	}
	return nil
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

// SetDefault replaces the `main-strong` slot's standard cell (setup).
func (s *Slots) SetDefault(ctx context.Context, pattern string) error {
	err := queries(s.db).SetDefaultSlot(ctx, modelsql.SetDefaultSlotParams{Profile: StandardProfile, Pattern: pattern, UpdatedAt: s.now().UTC()})
	return err
}

// Set maps a slot and profile, or clears the cell when pattern is empty.
// The mapping and audit receipt commit together. Callers authorize the change;
// who supplies attribution. Retired choices are refused as invalid input.
// Provider availability and access are not validated.
func (s *Slots) Set(ctx context.Context, slot, profile, pattern, who string) error {
	if err := retiredChoice(slot); err != nil {
		return err
	}
	if !isSlot(slot) {
		return errors.New("slot (no slash) required")
	}
	if profile == "" {
		profile = StandardProfile
	}
	if !ValidProfile(profile) {
		return errProfileName
	}
	at := s.now()
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		q := queries(tx)
		if pattern == "" {
			if _, err := q.DeleteSlotCell(ctx, modelsql.DeleteSlotCellParams{Slot: slot, Profile: profile}); err != nil {
				return err
			}
		} else if _, err := q.UpsertSlotCell(ctx, modelsql.UpsertSlotCellParams{Slot: slot, Profile: profile, Pattern: pattern, UpdatedAt: at.UTC()}); err != nil {
			return err
		}
		return audit.At(at).RecordIn(ctx, tx, who, ids.Nil, "model-slot.set", slot, map[string]any{"pattern": pattern, "profile": profile})
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
