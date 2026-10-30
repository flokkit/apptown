package wow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/wow/internal/wowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

// The recommended version (design/subsystems/ways-of-working-versions.md) is
// what new projects get and what projects already on a pack are offered as
// the update. Publishing never moves it, a stock pack's included, so a
// what-if can judge a release's new version first; a person moves it with
// standards:manage-packs, which no agent credential carries. No project
// moves: adopting stays a separate act.

// ErrNoPackVersion is a recommendation of a version the pack never had.
var ErrNoPackVersion = fault.New(fault.NotFound, "the pack has no such version")

// Recommend makes version the pack's recommended one, with its receipt.
// Recommending the version already recommended changes nothing.
//
// whatIf, when not zero, is the what-if the person decided on, which the
// caller has checked is of this pack and version; the receipt names it. A
// recommendation of the version already recommended moves nothing and
// records nothing; with a what-if it is ErrAlreadyRecommended, so the
// basis is never reported as recorded when it was not.
func (s *Packs) Recommend(ctx context.Context, name string, version int64, who string, whatIf ids.ID) error {
	now := s.now()
	return s.db.InTx(ctx, func(tx *database.Tx) error { return s.recommendIn(ctx, tx, now, name, version, who, whatIf) })
}

// ErrAlreadyRecommended is a recommendation, naming a what-if, of the
// version already recommended: nothing moved and nothing was recorded.
var ErrAlreadyRecommended = fault.New(fault.Conflict, "that version is already recommended; nothing changed and the what-if was not recorded")

// recommendIn recommends at the operation's time now.
func (s *Packs) recommendIn(ctx context.Context, tx *database.Tx, now time.Time, name string, version int64, who string, whatIf ids.ID) error {
	now = now.UTC()
	q := wowsql.New(tx.SQL())
	p, err := q.PackByNameForUpdate(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w %q", ErrNoPack, name)
	}
	if err != nil {
		return err
	}
	if _, err := q.PackVersion(ctx, wowsql.PackVersionParams{PackID: p.ID, Version: version}); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s v%d: %w", name, version, ErrNoPackVersion)
	} else if err != nil {
		return err
	}
	if p.RecommendedVersion == version {
		if !whatIf.IsZero() {
			return ErrAlreadyRecommended
		}
		return nil
	}
	if err := oneEdit(q.RecommendPackVersion(ctx, wowsql.RecommendPackVersionParams{ID: p.ID, Version: version, At: now})); err != nil {
		return err
	}
	receipt := map[string]any{"from": p.RecommendedVersion, "to": version}
	if !whatIf.IsZero() {
		receipt["whatIf"] = whatIf
	}
	return audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "pack.recommended", name, receipt)
}

// PackHistory is one pack as its page shows it: the author's declaration,
// every version with its lineage, and which version is recommended.
type PackHistory struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Origin      string        `json:"origin"`
	Declaration PackSpec      `json:"declaration"`
	Recommended int64         `json:"recommended"`
	Latest      int64         `json:"latest"`
	Versions    []Publication `json:"versions"`
}

// History reads a pack's page. Absence is ErrNoPack.
func (s *Packs) History(ctx context.Context, name string) (*PackHistory, error) {
	p, err := packByNameIn(ctx, wowsql.New(s.db.SQL()), name)
	if err != nil {
		return nil, err
	}
	versions, err := s.PublicationsOf(ctx, name)
	if err != nil {
		return nil, err
	}
	decl, _ := decodeSpec(string(p.SpecJson))
	return &PackHistory{Name: p.Name, Description: p.Description, Origin: p.Origin, Declaration: decl,
		Recommended: p.RecommendedVersion, Latest: p.LatestVersion, Versions: versions}, nil
}
