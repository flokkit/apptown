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

// packRow is one row of packs.
type packRow struct {
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
func (p packRow) Platform() bool { return specIsPlatform(p.SpecJSON) }

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
	errStockPack  = fault.New(fault.Conflict, "a stock pack re-syncs with the release; make a pack of your own beside it, and take both")
	errPackOwner  = errors.New("pack belongs to another origin")
	errLastPack   = fault.New(fault.Conflict, "the last pack that is on cannot be turned off; turn another on first, or projects could not be made")
	errPackAbsent = errors.New("no pack named")
	// errNoPack is the absence of the pack a caller addressed by name. It is
	// classified here so callers never need to know the store answers
	// absence with sql.ErrNoRows.
	errNoPack = fault.New(fault.NotFound, "no pack is named")
)

// packByNameIn reads the pack a caller addressed; absence is ErrNoPack.
func packByNameIn(ctx context.Context, q *wowsql.Queries, name string) (wowsql.Pack, error) {
	p, err := q.PackByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return p, fmt.Errorf("%w %q", errNoPack, name)
	}
	return p, err
}

// packs is the supported API over pack rows and their publications.
type packs struct {
	db    *database.DB
	clock func() time.Time // AppTown's clock; nil is the real one
	// lines is the tool versions list a pack's tools are held to; nil
	// refuses every line.
	lines toolLines
	// shipped names the update stock versions come from (source and signer, origin.go).
	shipped string
}

func (s *packs) now() time.Time { return clock.Or(s.clock)() }

func NewPacks(db *database.DB) *packs { return &packs{db: db} }

func (s *packs) ByName(ctx context.Context, name string) (*packRow, error) {
	r, err := wowsql.New(s.db.SQL()).PackByName(ctx, name)
	if err != nil {
		return nil, err
	}
	p := packRecord(r)
	return &p, nil
}

// all is every pack, stock first.
func (s *packs) all(ctx context.Context) ([]packRow, error) {
	rows, err := wowsql.New(s.db.SQL()).Packs(ctx)
	var out []packRow
	for _, r := range rows {
		out = append(out, packRecord(r))
	}
	return out, err
}

// published is every pack with at least one publication.
func (s *packs) published(ctx context.Context) ([]packRow, error) {
	all, err := s.all(ctx)
	var out []packRow
	for _, p := range all {
		if p.LatestVersion > 0 {
			out = append(out, p)
		}
	}
	return out, err
}

// offeredPack is a pack row read with the resolved contents of the
// publication it offers to new projects and as the update: what applying
// it installs. The row's own spec is the author's declaration.
func offeredPack(r wowsql.Pack, spec json.RawMessage, version int64) (Pack, error) {
	pk, err := packFrom(packRecord(r), string(spec))
	pk.Version = version
	return pk, err
}

// offeredContents is every pack with its offered publication's contents,
// stock first.
func (s *packs) offeredContents(ctx context.Context) ([]Pack, error) {
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

// offeredContentsOf is one pack with its offered publication's contents.
func (s *packs) offeredContentsOf(ctx context.Context, name string) (*Pack, error) {
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
		return PackVersion{}, fmt.Errorf("%w %q", errPackAbsent, name)
	}
	if err != nil {
		return PackVersion{}, err
	}
	if r.ID.IsZero() {
		return PackVersion{}, fmt.Errorf("%s has no version %d", name, version)
	}
	return PackVersion{SpecJSON: string(r.SpecJson), PinsJSON: string(r.PinsJson)}, nil
}

// Publish records a pack's declaration as a version: version 1 for a new
// name; the next version when the pinned contents or the declaration
// moved; only the words when none did. A name another origin owns is
// refused, and so is a declaration naming an item the library lacks.
func (s *packs) Publish(ctx context.Context, name, description, declaration, origin string) (int, error) {
	return s.publishNamed(ctx, name, description, declaration, origin, "")
}

// publishNamed publishes like Publish and names the new version. A new
// pack's first version is its recommended one; a later version, stock or
// not, is not recommended until a person says so, so a what-if can judge it
// before projects are offered it.
func (s *packs) publishNamed(ctx context.Context, name, description, declaration, origin, versionName string) (int, error) {
	var version int
	now := s.now()
	err := s.db.InTx(ctx, func(tx *database.Tx) error {
		v, err := s.publishIn(ctx, tx, now, name, description, declaration, origin, cut{refuseMissing: true, name: versionName})
		version = int(v)
		return err
	})
	return version, err
}

// cut is how one publication is made: whether items the library lacks
// refuse it, the note it carries, and its name.
type cut struct {
	refuseMissing bool
	note          string
	name          string
}

// publishIn is the one publication rule, inside the caller's transaction,
// under the pack's row lock, at the operation's time now.
func (s *packs) publishIn(ctx context.Context, tx *database.Tx, now time.Time, name, description, declaration, origin string, how cut) (int64, error) {
	q := wowsql.New(tx.SQL())
	p, err := q.PackByNameForUpdate(ctx, name)
	fresh := errors.Is(err, sql.ErrNoRows)
	if err != nil && !fresh {
		return 0, err
	}
	if !fresh && p.Origin != origin {
		return 0, fmt.Errorf("pack %s: %w %q", name, errPackOwner, p.Origin)
	}
	res := s.resolve(ctx, declaration)
	// A version keeps what it pinned; an item it cannot pin would be
	// whatever later takes the name, so a save names only what exists.
	if how.refuseMissing && len(res.Missing) > 0 {
		return 0, fmt.Errorf("%w: the library has no %s; add it first, or leave it out of the pack", errUnpinned, strings.Join(res.Missing, ", "))
	}
	if err := s.checkTools(res.Spec); err != nil {
		return 0, fmt.Errorf("pack %s: %w", name, err)
	}
	// A version stores the pinned contents, every policy with the
	// definition it pins. A declaration that does not decode (a historical
	// representation) is stored as written, never replaced by a partial
	// reading of it.
	spec := declaration
	if _, derr := decodeSpec(declaration); derr == nil {
		if spec, err = strs.JSON(res.Spec); err != nil {
			return 0, err
		}
	}
	pins, err := strs.JSON(res.Pins)
	if err != nil {
		return 0, err
	}
	pins = canonJSON(pins)
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
		// A version names exact contents.
		if canonJSON(string(current.SpecJson)) == canonJSON(spec) && canonJSON(string(current.PinsJson)) == pins &&
			canonJSON(string(current.DeclarationJson)) == canonJSON(declaration) {
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
		Declaration: []byte(declaration), Note: note, Name: how.name, BasedOn: basedOn, At: now, Source: s.origin(origin).Source, Signer: s.origin(origin).Signer}); err != nil {
		return 0, err
	}
	if _, err := q.AdvancePackVersion(ctx, wowsql.AdvancePackVersionParams{ID: p.ID, Version: version, Description: description, Spec: []byte(declaration), At: now}); err != nil {
		return 0, err
	}
	return version, nil
}

// resolve pins a declaration's items at the library's current versions.
// A declaration that does not decode is resolved as what of it does.
func (s *packs) resolve(ctx context.Context, declaration string) resolution {
	d, _ := decodeSpec(declaration)
	return resolveDeclaration(ctx, d, libraryPins{lib: NewLibrary(s.db), wf: NewWorkflows(s.db), pol: NewPolicies(s.db)})
}

// libraryPins reads own items' current pins from the library.
type libraryPins struct {
	lib *library
	wf  *workflows
	pol *policies
}

func (l libraryPins) BlockPin(ctx context.Context, name string) (int, bool) {
	b, err := l.lib.ByName(ctx, name)
	if err != nil {
		return 0, false
	}
	return b.LatestVersion, true
}

func (l libraryPins) WorkflowPin(ctx context.Context, name string, platform bool) (int, bool, bool) {
	v, _, err := l.wf.version(ctx, l.wf.SystemFor(ctx, name), 0)
	if err != nil {
		return 0, false, false
	}
	if !platform && l.wf.isSystem(ctx, name) {
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

// describe refreshes a stock pack's words without a new version.
func (s *packs) describe(ctx context.Context, name, description string) error {
	_, err := wowsql.New(s.db.SQL()).RefreshStockPackWords(ctx, wowsql.RefreshStockPackWordsParams{Name: name, Description: description})
	return err
}

// setEnabled is the instance's switch, with its receipt. The last pack
// that is on cannot be turned off: a project must be makeable from
// something. An unknown name is ErrNoPack.
func (s *packs) setEnabled(ctx context.Context, name string, enabled bool, who string) error {
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
				return fmt.Errorf("%q: %w", name, errLastPack)
			}
		}
		if _, err := q.SetPackEnabled(ctx, wowsql.SetPackEnabledParams{Name: name, Enabled: enabled, At: now}); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "pack.enabled", name, map[string]any{"enabled": enabled})
	})
}

// delete removes a custom pack with its receipt; stock is refused with
// ErrStockPack, absence is ErrNoPack.
func (s *packs) delete(ctx context.Context, name, who string) error {
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		q := wowsql.New(tx.SQL())
		p, err := packByNameIn(ctx, q, name)
		if err != nil {
			return err
		}
		if p.Origin == "stock" {
			return fmt.Errorf("%q: %w", name, errStockPack)
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

// publicationRow is one entry of a pack's version history.
type publicationRow struct {
	Version int64
	Note    string
	At      stamp.Time
}

// publications is a pack's versions, newest first.
func (s *packs) publications(ctx context.Context, packID ids.ID) ([]publicationRow, error) {
	rows, err := wowsql.New(s.db.SQL()).PackVersions(ctx, packID)
	out := make([]publicationRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, publicationRow{Version: r.Version, Note: r.Note, At: stamp.Time{NullTime: sql.NullTime{Time: r.CreatedAt, Valid: true}}})
	}
	return out, err
}

func packRecord(r wowsql.Pack) packRow {
	return packRow{ID: r.ID, Name: r.Name, Description: r.Description, SpecJSON: string(r.SpecJson), Origin: r.Origin, Enabled: r.Enabled, LatestVersion: r.LatestVersion, RecommendedVersion: r.RecommendedVersion}
}

// PackByNameIn reads catalog identity within a caller's transaction.
func PackByNameIn(ctx context.Context, q database.Queryer, name string) (packRow, error) {
	r, e := wowsql.New(q.SQL()).PackByName(ctx, name)
	return packRecord(r), e
}
