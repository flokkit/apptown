package wow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/wow/internal/wowsql"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

// Block is one row of guidance_blocks, with the shown version's size.
type Block struct {
	ID            ids.ID `json:"id"`
	Name          string `json:"name"`
	Tags          string `json:"tags"`
	LatestVersion int    `json:"latestVersion"`
	BodyMD        string `json:"bodyMd,omitempty"`
	// Origin is "stock" for an AppTown-shipped block (read-only in the UI; fork
	// it by making your own pack) or "" for an institution-authored one.
	Origin    string `json:"origin"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	// Bytes is the size of the shown version's body: every block an agent
	// is handed costs context, so the size sits beside the name.
	Bytes int `json:"bytes"`
	// Tier is '' or platform (AppTown's own contract, on every
	// repo-backed project); Conflicts is a comma list of blocks this one
	// contradicts.
	Tier      string `json:"tier,omitempty"`
	Conflicts string `json:"conflicts,omitempty"`
	// Withdrawn: stock the running release no longer ships. It is not
	// offered; a project that adopted it keeps it (withdrawn.go).
	Withdrawn bool `json:"withdrawn,omitempty"`
}

// Pin is what a manifest asks for: a block by name, at a version (0 = the
// latest), possibly disabled.
type Pin struct {
	Name     string `json:"name"`
	Version  int    `json:"version"`
	Disabled bool   `json:"disabled"`
}

// BlockVersion is one entry of a block's history.
type BlockVersion struct {
	Version   int    `json:"version"`
	CreatedAt string `json:"createdAt"`
	// Origin is where the version came from (origin.go).
	Origin
}

// BlockFilter narrows a listing: a substring of the name or body, a tag.
type BlockFilter struct{ Query, Tag string }

var (
	blockNameRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(/[a-z0-9][a-z0-9-]*)*$`)
	ErrBlockName      = fault.New(fault.Invalid, "block name must look like prose/banned-phrases")
	ErrUnknownBlock   = fault.New(fault.Unprocessable, "unknown block")
	ErrNoSuchVersion  = fault.New(fault.Unprocessable, "no such version")
	ErrImportedTwice  = fault.New(fault.Unprocessable, "block imported twice")
	errBlockNameShape = blockNameRe.String()
)

// NameShape is the shape a block or catalog name must have, for messages.
const NameShape = `^[a-z0-9][a-z0-9-]*(/[a-z0-9][a-z0-9-]*)*$`

// ValidName reports whether a name has the library's shape (blocks and
// catalog changes share it: prose/banned-phrases, uplift/go-version).
func ValidName(name string) bool { return blockNameRe.MatchString(name) }

// Library is the supported API over guidance blocks, versions and imports.
type Library struct {
	db *database.DB
	// stock is where the stock sync's versions came from (source and signer, origin.go).
	stock Origin
	// clock is AppTown's clock, which stamps a block version: an
	// adoption resolves a block as of its own time, by the same clock.
	// nil is the real one.
	clock func() time.Time
}

func (s *Library) now() time.Time { return clock.Or(s.clock)() }

func NewLibrary(db *database.DB) *Library { return &Library{db: db, stock: shippedOrigin("")} }

func (s *Library) ByID(ctx context.Context, id ids.ID) (*Block, error) {
	r, err := wowsql.New(s.db.SQL()).BlockByID(ctx, id)
	if err != nil {
		return nil, err
	}
	b := blockRecord(r)
	return &b, nil
}

func (s *Library) ByName(ctx context.Context, name string) (*Block, error) {
	r, err := wowsql.New(s.db.SQL()).BlockByName(ctx, name)
	if err != nil {
		return nil, err
	}
	b := blockRecord(r)
	return &b, nil
}

// VersionBody is the body of one version of a block.
func (s *Library) VersionBody(ctx context.Context, blockID ids.ID, version int) (string, error) {
	r, err := wowsql.New(s.db.SQL()).BlockVersion(ctx, wowsql.BlockVersionParams{BlockID: blockID, Version: int64(version)})
	return string(r.BodyMd), err
}

// All lists blocks by name with their latest version's size, narrowed by
// the filter.
func (s *Library) All(ctx context.Context, f BlockFilter) ([]Block, error) {
	rows, err := wowsql.New(s.db.SQL()).BlocksMatchingMeta(ctx, wowsql.BlocksMatchingMetaParams{Query: f.Query, Tag: f.Tag})
	out := make([]Block, 0, len(rows))
	for _, r := range rows {
		b := blockRecord(r.GuidanceBlock)
		b.Bytes = int(r.BodyBytes)
		out = append(out, b)
	}
	return out, err
}

// BytesByName is the latest-version size of each named block, for pack
// cards and pickers. Unknown names are absent.
func (s *Library) BytesByName(ctx context.Context, names []string) map[string]int {
	out := map[string]int{}
	if len(names) == 0 {
		return out
	}
	rows, err := s.All(ctx, BlockFilter{})
	if err != nil {
		return out
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	for _, r := range rows {
		if want[r.Name] {
			out[r.Name] = r.Bytes
		}
	}
	return out
}

// Create records a new institution-authored block at version 1 with its
// receipt, in one transaction.
func (s *Library) Create(ctx context.Context, name, body, tags, who string) (*Block, error) {
	if !blockNameRe.MatchString(name) {
		return nil, fmt.Errorf("%w (%s)", ErrBlockName, errBlockNameShape)
	}
	if _, _, err := SplitSkill(name, body); err != nil {
		return nil, err
	}
	var block Block
	now := s.now().UTC()
	err := s.db.InTx(ctx, func(tx *database.Tx) error {
		q := wowsql.New(tx.SQL())
		r, err := q.InsertBlock(ctx, wowsql.InsertBlockParams{ID: ids.New(now), Name: name, Tags: NormalizeTags(tags), At: now})
		if err != nil {
			return err
		}
		block = blockRecord(r)
		if _, err := q.InsertBlockVersion(ctx, wowsql.InsertBlockVersionParams{ID: ids.New(now), BlockID: r.ID, Version: 1, Body: body, Checks: []byte("[]"), At: now, Source: AuthoredHere.Source, Signer: AuthoredHere.Signer}); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "guidance.block.created", name, map[string]any{"blockId": r.ID})
	})
	if err != nil {
		return nil, err
	}
	return &block, nil
}

// Versions is a block's history, newest first.
func (s *Library) Versions(ctx context.Context, blockID ids.ID) ([]BlockVersion, error) {
	rows, err := wowsql.New(s.db.SQL()).BlockVersionsMeta(ctx, blockID)
	out := make([]BlockVersion, 0, len(rows))
	for _, r := range rows {
		out = append(out, BlockVersion{Version: int(r.Version), CreatedAt: r.CreatedAt.Format(time.RFC3339Nano), Origin: Origin{Source: r.Source, Signer: r.Signer}})
	}
	return out, err
}

// Bump appends a version to a block under its row lock, so two concurrent
// edits get two versions; the version, the pointer and the receipt commit
// together. checksJSON may be empty.
func (s *Library) Bump(ctx context.Context, id ids.ID, body, checksJSON, who, name string) error {
	if strings.TrimSpace(checksJSON) == "" {
		checksJSON = "[]"
	}
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		q := wowsql.New(tx.SQL())
		r, err := q.BlockByIDForUpdate(ctx, id)
		if err != nil {
			return err
		}
		return bumpBlockIn(ctx, tx, r, body, checksJSON, who, s.now())
	})
}

// ResolvedPin is a pin the library has checked: the block exists and has
// the version (the latest when none was asked for).
type ResolvedPin struct {
	BlockID  ids.ID
	Name     string
	Version  int
	Disabled bool
}

// Resolve checks a manifest's pins: no block twice, every block known,
// every version real. Errors name the block.
func (s *Library) Resolve(ctx context.Context, pins []Pin) ([]ResolvedPin, error) {
	seen := map[string]bool{}
	var out []ResolvedPin
	for _, pin := range pins {
		if seen[pin.Name] {
			return nil, fmt.Errorf("%w: %q", ErrImportedTwice, pin.Name)
		}
		seen[pin.Name] = true
		b, err := s.ByName(ctx, pin.Name)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %q", ErrUnknownBlock, pin.Name)
		} else if err != nil {
			return nil, err
		}
		v := pin.Version
		if v == 0 {
			v = b.LatestVersion
		}
		if v < 1 || v > b.LatestVersion {
			return nil, fmt.Errorf("%w: block %q has no version %d (latest is %d)", ErrNoSuchVersion, pin.Name, v, b.LatestVersion)
		}
		out = append(out, ResolvedPin{BlockID: b.ID, Name: pin.Name, Version: v, Disabled: pin.Disabled})
	}
	return out, nil
}

// NormalizeTags trims a comma list and drops empty entries.
func NormalizeTags(tags string) string {
	var out []string
	for _, tag := range strings.Split(tags, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			out = append(out, tag)
		}
	}
	return strings.Join(out, ",")
}

// Names lists the blocks whose names start with any of the prefixes
// (skill/, resource/): what the workflow assistant may name.
func (s *Library) Names(ctx context.Context, prefixes ...string) ([]string, error) {
	all, err := s.All(ctx, BlockFilter{})
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, b := range all {
		for _, p := range prefixes {
			if strings.HasPrefix(b.Name, p) {
				out = append(out, b.Name)
				break
			}
		}
	}
	return out, nil
}

// VersionChecks is the checks a block version declares, as stored JSON
// ("[]" when none).
func (s *Library) VersionChecks(ctx context.Context, blockID ids.ID, version int) (string, error) {
	r, err := wowsql.New(s.db.SQL()).BlockVersion(ctx, wowsql.BlockVersionParams{BlockID: blockID, Version: int64(version)})
	return string(r.ChecksJson), err
}

func blockRecord(r wowsql.GuidanceBlock) Block {
	return Block{ID: r.ID, Name: r.Name, Tags: r.Tags, LatestVersion: int(r.LatestVersion), Origin: r.Origin,
		CreatedAt: r.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: r.UpdatedAt.Format(time.RFC3339Nano), Tier: r.Tier, Conflicts: r.Conflicts}
}
