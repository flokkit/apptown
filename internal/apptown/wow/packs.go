package wow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/wow/internal/wowsql"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/stamp"
	"github.com/flokkit/apptown/internal/strs"
)

// PackRecord is one row of packs.
type PackRecord struct {
	ID            ids.ID
	Name          string
	Description   string
	SpecJSON      string
	Origin        string // "stock" = vendor (read-only, re-synced); "" = custom
	Enabled       bool
	LatestVersion int64
	// RecommendedVersion is what new projects get and what projects are
	// offered as the update; only a person moves it.
	RecommendedVersion int64
}

// Platform reports whether the spec declares AppTown's own tier.
func (p PackRecord) Platform() bool { return specIsPlatform(p.SpecJSON) }

func specIsPlatform(spec string) bool {
	var s struct {
		Platform bool `json:"platform"`
	}
	_ = json.Unmarshal([]byte(spec), &s)
	return s.Platform
}

// PackVersion is one publication: the spec and the pins as JSON text.
type PackVersion struct{ SpecJSON, PinsJSON string }

var (
	ErrStockPack  = fault.New(fault.Conflict, "a stock pack re-syncs with the release; make a pack of your own that includes it")
	ErrPackOwner  = errors.New("pack belongs to another origin")
	ErrLastPack   = fault.New(fault.Conflict, "the last pack that is on cannot be turned off; turn another on first, or projects could not be made")
	ErrPackAbsent = errors.New("no pack named")
	// ErrNoPack is the absence of the pack a caller addressed by name. It is
	// classified here so callers never need to know the store answers
	// absence with sql.ErrNoRows.
	ErrNoPack = fault.New(fault.NotFound, "no pack is named")
)

// packByNameIn reads the pack a caller addressed; absence is ErrNoPack.
func packByNameIn(ctx context.Context, q *wowsql.Queries, name string) (wowsql.Pack, error) {
	p, err := q.PackByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return p, fmt.Errorf("%w %q", ErrNoPack, name)
	}
	return p, err
}

// Packs is the supported API over pack rows and their publications.
type Packs struct {
	db    *database.DB
	clock func() time.Time // AppTown's clock; nil is the real one
}

func (s *Packs) now() time.Time { return clock.Or(s.clock)() }

func NewPacks(db *database.DB) *Packs { return &Packs{db: db} }

func (s *Packs) ByName(ctx context.Context, name string) (*PackRecord, error) {
	r, err := wowsql.New(s.db.SQL()).PackByName(ctx, name)
	if err != nil {
		return nil, err
	}
	p := packRecord(r)
	return &p, nil
}

// All is every pack, stock first.
func (s *Packs) All(ctx context.Context) ([]PackRecord, error) {
	rows, err := wowsql.New(s.db.SQL()).Packs(ctx)
	var out []PackRecord
	for _, r := range rows {
		out = append(out, packRecord(r))
	}
	return out, err
}

// Published is every pack with at least one publication.
func (s *Packs) Published(ctx context.Context) ([]PackRecord, error) {
	all, err := s.All(ctx)
	var out []PackRecord
	for _, p := range all {
		if p.LatestVersion > 0 {
			out = append(out, p)
		}
	}
	return out, err
}

// Offered is the packs a new project may be given: on, and not the
// platform tier. Everything that offers a pack reads this.
func (s *Packs) Offered(ctx context.Context) ([]PackRecord, error) {
	all, err := s.All(ctx)
	if err != nil {
		return nil, err
	}
	var out []PackRecord
	for _, p := range all {
		if p.Enabled && !p.Platform() {
			out = append(out, p)
		}
	}
	return out, nil
}

// offeredPack is a pack row read with the resolved contents of the
// publication it offers to new projects and as the update: what applying
// it installs. The row's own spec is the author's declaration.
func offeredPack(r wowsql.Pack, spec json.RawMessage, version int64) (Pack, error) {
	pk, err := packFrom(packRecord(r), string(spec))
	pk.Version = version
	return pk, err
}

// OfferedContents is every pack with its offered publication's contents,
// stock first.
func (s *Packs) OfferedContents(ctx context.Context) ([]Pack, error) {
	rows, err := wowsql.New(s.db.SQL()).OfferedPacks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Pack, 0, len(rows))
	for _, r := range rows {
		pk, err := offeredPack(r.Pack, r.OfferedSpec, r.OfferedVersion)
		if err != nil {
			return nil, err
		}
		out = append(out, pk)
	}
	return out, nil
}

// OfferedContentsOf is one pack with its offered publication's contents.
func (s *Packs) OfferedContentsOf(ctx context.Context, name string) (*Pack, error) {
	r, err := wowsql.New(s.db.SQL()).OfferedPack(ctx, name)
	if err != nil {
		return nil, err
	}
	pk, err := offeredPack(r.Pack, r.OfferedSpec, r.OfferedVersion)
	if err != nil {
		return nil, err
	}
	return &pk, nil
}

// Version is one publication; a named publication is immutable, never
// the pack's latest in its place.
func PackVersionIn(ctx context.Context, q database.Queryer, name string, version int64) (PackVersion, error) {
	r, err := wowsql.New(q.SQL()).PackPublication(ctx, wowsql.PackPublicationParams{Name: name, Version: version})
	if errors.Is(err, sql.ErrNoRows) {
		return PackVersion{}, fmt.Errorf("%w %q", ErrPackAbsent, name)
	}
	if err != nil {
		return PackVersion{}, err
	}
	if r.ID.IsZero() {
		return PackVersion{}, fmt.Errorf("%s has no version %d", name, version)
	}
	return PackVersion{SpecJSON: string(r.SpecJson), PinsJSON: string(r.PinsJson)}, nil
}

// LatestPins is the latest publication's pins, "" when there is none.
func (s *Packs) LatestPins(ctx context.Context, p PackRecord) (string, error) {
	r, err := wowsql.New(s.db.SQL()).PackVersion(ctx, wowsql.PackVersionParams{PackID: p.ID, Version: p.LatestVersion})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return string(r.PinsJson), err
}

// Publish records a pack's declaration as a version: version 1 for a new
// name; the next version when the resolved contents, the pins or the
// declaration moved; only the words when none did. A name another origin
// owns is refused, and so is a declaration that replaces or drops an item
// the included publication does not carry.
func (s *Packs) Publish(ctx context.Context, name, description, declaration, origin string) (int, error) {
	return s.PublishNamed(ctx, name, description, declaration, origin, "")
}

// PublishNamed publishes like Publish and names the new version. A new
// pack's first version is its recommended one; a later version, stock or
// not, is not recommended until a person says so, so a what-if can judge it
// before projects are offered it.
func (s *Packs) PublishNamed(ctx context.Context, name, description, declaration, origin, versionName string) (int, error) {
	var version int
	now := s.now()
	err := s.db.InTx(ctx, func(tx *database.Tx) error {
		v, err := s.publishIn(ctx, tx, now, name, description, declaration, origin, cut{refuseGone: true, name: versionName})
		version = int(v)
		return err
	})
	return version, err
}

// cut is how one publication is made: which included version it resolves
// against (0: a pin in the declaration, else the included pack's offered
// version), whether items gone from the included publication refuse it,
// and the note it carries.
type cut struct {
	includedVersion int64
	refuseGone      bool
	note            string
	name            string
}

// publishIn is the one publication rule, inside the caller's transaction,
// under the pack's row lock, at the operation's time now.
func (s *Packs) publishIn(ctx context.Context, tx *database.Tx, now time.Time, name, description, declaration, origin string, how cut) (int64, error) {
	q := wowsql.New(tx.SQL())
	p, err := q.PackByNameForUpdate(ctx, name)
	fresh := errors.Is(err, sql.ErrNoRows)
	if err != nil && !fresh {
		return 0, err
	}
	if !fresh && p.Origin != origin {
		return 0, fmt.Errorf("pack %s: %w %q", name, ErrPackOwner, p.Origin)
	}
	res, err := s.resolveIn(ctx, q, name, declaration, how.includedVersion)
	if err != nil {
		return 0, err
	}
	if how.refuseGone && len(res.Gone) > 0 {
		return 0, fmt.Errorf("%w: %s does not carry %s, which this pack replaces or drops", ErrInclusion, res.Included.Pack, strings.Join(res.Gone, ", "))
	}
	// A version keeps what it pinned; an item it cannot pin would be
	// whatever later takes the name, so a save names only what exists.
	if how.refuseGone && len(res.Missing) > 0 {
		return 0, fmt.Errorf("%w: the library has no %s; add it first, or leave it out of the pack", ErrUnpinned, strings.Join(res.Missing, ", "))
	}
	// A version stores the resolved contents: inherited items from the
	// included publication, and every policy with the definition it pins.
	// A declaration that does not decode (a historical representation) is
	// stored as written, never replaced by a partial reading of it.
	spec := declaration
	if _, derr := decodeSpec(declaration); derr == nil || res.Included != nil {
		if spec, err = strs.JSON(res.Spec); err != nil {
			return 0, err
		}
	}
	pins, err := strs.JSON(res.Pins)
	if err != nil {
		return 0, err
	}
	pins = canonJSON(pins)
	includedPack, includedVersion := "", int64(0)
	if res.Included != nil {
		includedPack, includedVersion = res.Included.Pack, res.Included.Version
	}
	note := how.note
	if note == "" {
		note = "published contents"
	}
	now = now.UTC()
	var version, basedOn int64 = 1, 0
	if fresh {
		if p, err = q.InsertPack(ctx, wowsql.InsertPackParams{ID: ids.New(now), Name: name, Description: description, Spec: []byte(declaration), Origin: origin, At: now}); err != nil {
			return 0, err
		}
	} else {
		current, err := q.PackVersion(ctx, wowsql.PackVersionParams{PackID: p.ID, Version: p.LatestVersion})
		if err != nil {
			return 0, fmt.Errorf("pack %s v%d is missing its publication: %w", name, p.LatestVersion, err)
		}
		// A version names exact contents and the exact publication it
		// included: a follow of a new recommendation is a new version even
		// when the items it inherits read the same.
		if canonJSON(string(current.SpecJson)) == canonJSON(spec) && canonJSON(string(current.PinsJson)) == pins &&
			canonJSON(string(current.DeclarationJson)) == canonJSON(declaration) &&
			current.IncludedPack == includedPack && current.IncludedVersion == includedVersion {
			_, err := q.RefreshPackContents(ctx, wowsql.RefreshPackContentsParams{ID: p.ID, Description: description, Spec: []byte(declaration)})
			return p.LatestVersion, err
		}
		rows, err := q.PackVersions(ctx, p.ID)
		if err != nil {
			return 0, err
		}
		version = rows[0].Version + 1 // after every version, an amendment of an older one too
		basedOn = p.LatestVersion
	}
	if _, err := q.InsertPackVersion(ctx, wowsql.InsertPackVersionParams{ID: ids.New(now), PackID: p.ID, Version: version, Spec: []byte(spec), Pins: []byte(pins),
		Declaration: []byte(declaration), IncludedPack: includedPack, IncludedVersion: includedVersion, Note: note, Name: how.name, BasedOn: basedOn, At: now}); err != nil {
		return 0, err
	}
	if _, err := q.AdvancePackVersion(ctx, wowsql.AdvancePackVersionParams{ID: p.ID, Version: version, Description: description, Spec: []byte(declaration), At: now}); err != nil {
		return 0, err
	}
	return version, nil
}

// resolveIn resolves a declaration against the exact publication it
// includes: a pin in the declaration, else includedVersion when the caller
// names one, else the included pack's offered version. An include chain
// that reaches back to the pack itself is refused.
func (s *Packs) resolveIn(ctx context.Context, q *wowsql.Queries, self, declaration string, includedVersion int64) (resolution, error) {
	d, err := decodeSpec(declaration)
	lib := libraryPins{lib: NewLibrary(s.db), wf: NewWorkflows(s.db), pol: NewPolicies(s.db)}
	var include struct {
		Includes string `json:"includes"`
	}
	_ = json.Unmarshal([]byte(declaration), &include)
	if include.Includes == "" {
		// Its pins read what the spec says that decodes.
		return resolveDeclaration(ctx, d, nil, lib)
	}
	if err != nil {
		return resolution{}, fmt.Errorf("%w: %w", ErrInclusion, err)
	}
	name, pinned, err := IncludeRef(d.Includes)
	if err != nil {
		return resolution{}, err
	}
	if name == self {
		return resolution{}, fmt.Errorf("%w: a pack cannot include itself", ErrInclusion)
	}
	version := pinned
	if version == 0 {
		version = includedVersion
	}
	if version == 0 {
		inc, err := packByNameIn(ctx, q, name)
		if err != nil {
			return resolution{}, fmt.Errorf("%w: %w", ErrInclusion, err)
		}
		version = offeredVersion(inc)
	}
	inc, err := q.PackPublication(ctx, wowsql.PackPublicationParams{Name: name, Version: version})
	if errors.Is(err, sql.ErrNoRows) || (err == nil && inc.ID.IsZero()) {
		return resolution{}, fmt.Errorf("%w: %s has no version %d", ErrInclusion, name, version)
	}
	if err != nil {
		return resolution{}, err
	}
	for at, atVersion, hops := inc.IncludedPack, inc.IncludedVersion, 0; at != ""; hops++ {
		if at == self || hops > 32 {
			return resolution{}, fmt.Errorf("%w: %s includes %s, so %s cannot include it", ErrInclusion, name, self, self)
		}
		next, err := q.PackPublication(ctx, wowsql.PackPublicationParams{Name: at, Version: atVersion})
		if err != nil {
			return resolution{}, err
		}
		at, atVersion = next.IncludedPack, next.IncludedVersion
	}
	spec, err := decodeSpec(string(inc.SpecJson))
	if err != nil {
		return resolution{}, err
	}
	var pins PinSet
	if err := json.Unmarshal(inc.PinsJson, &pins); err != nil {
		return resolution{}, err
	}
	return resolveDeclaration(ctx, d, &includedPublication{Pack: name, Version: version, Spec: spec, Pins: pins}, lib)
}

// offeredVersion is the publication a pack offers to new projects and as
// the update.
func offeredVersion(p wowsql.Pack) int64 { return p.RecommendedVersion }

// libraryPins reads own items' current pins from the library.
type libraryPins struct {
	lib *Library
	wf  *Workflows
	pol *Policies
}

func (l libraryPins) BlockPin(ctx context.Context, name string) (int, bool) {
	b, err := l.lib.ByName(ctx, name)
	if err != nil {
		return 0, false
	}
	return b.LatestVersion, true
}

func (l libraryPins) WorkflowPin(ctx context.Context, name string, platform bool) (int, bool, bool) {
	v, _, err := l.wf.Version(ctx, l.wf.SystemFor(ctx, name), 0)
	if err != nil {
		return 0, false, false
	}
	if !platform && l.wf.IsSystem(ctx, name) {
		return 0, false, true
	}
	return v, true, true
}

func (l libraryPins) PolicyPin(ctx context.Context, name string) (PackPolicy, bool) {
	lp := l.pol.LibraryByName(ctx, name)
	if lp == nil {
		return PackPolicy{}, false
	}
	return PackPolicy{Name: lp.Name, Description: lp.Description, Rule: lp.Rule, With: lp.With}, true
}

// Describe refreshes a stock pack's words without a new version.
func (s *Packs) Describe(ctx context.Context, name, description string) error {
	_, err := wowsql.New(s.db.SQL()).RefreshStockPackWords(ctx, wowsql.RefreshStockPackWordsParams{Name: name, Description: description})
	return err
}

// SetEnabled is the instance's switch, with its receipt. The last pack
// that is on cannot be turned off: a project must be makeable from
// something. An unknown name is ErrNoPack.
func (s *Packs) SetEnabled(ctx context.Context, name string, enabled bool, who string) error {
	now := s.now().UTC()
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		q := wowsql.New(tx.SQL())
		if err := q.LockPacks(ctx); err != nil {
			return err
		}
		p, err := packByNameIn(ctx, q, name)
		if err != nil {
			return err
		}
		if !enabled && p.Enabled && !specIsPlatform(string(p.SpecJson)) {
			others, err := q.OtherOfferedPackCount(ctx, name)
			if err != nil {
				return err
			}
			if others == 0 {
				return fmt.Errorf("%q: %w", name, ErrLastPack)
			}
		}
		if _, err := q.SetPackEnabled(ctx, wowsql.SetPackEnabledParams{Name: name, Enabled: enabled, At: now}); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "pack.enabled", name, map[string]any{"enabled": enabled})
	})
}

// Delete removes a custom pack with its receipt; stock is refused with
// ErrStockPack, absence is ErrNoPack.
func (s *Packs) Delete(ctx context.Context, name, who string) error {
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		q := wowsql.New(tx.SQL())
		p, err := packByNameIn(ctx, q, name)
		if err != nil {
			return err
		}
		if p.Origin == "stock" {
			return fmt.Errorf("%q: %w", name, ErrStockPack)
		}
		if _, err := q.DeletePack(ctx, name); err != nil {
			return err
		}
		return audit.At(s.now()).RecordIn(ctx, tx, who, ids.Nil, "pack.deleted", name, nil)
	})
}

func canonJSON(s string) string {
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// PublicationRow is one entry of a pack's version history.
type PublicationRow struct {
	Version int64
	Note    string
	At      stamp.Time
}

// Publications is a pack's versions, newest first.
func (s *Packs) Publications(ctx context.Context, packID ids.ID) ([]PublicationRow, error) {
	rows, err := wowsql.New(s.db.SQL()).PackVersions(ctx, packID)
	out := make([]PublicationRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, PublicationRow{Version: r.Version, Note: r.Note, At: stamp.Time{NullTime: sql.NullTime{Time: r.CreatedAt, Valid: true}}})
	}
	return out, err
}

func packRecord(r wowsql.Pack) PackRecord {
	return PackRecord{ID: r.ID, Name: r.Name, Description: r.Description, SpecJSON: string(r.SpecJson), Origin: r.Origin, Enabled: r.Enabled, LatestVersion: r.LatestVersion, RecommendedVersion: r.RecommendedVersion}
}

// PackByNameIn reads catalog identity within a caller's transaction.
func PackByNameIn(ctx context.Context, q database.Queryer, name string) (PackRecord, error) {
	r, e := wowsql.New(q.SQL()).PackByName(ctx, name)
	return packRecord(r), e
}
