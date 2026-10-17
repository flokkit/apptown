package wow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/wow/internal/wowsql"
	"flokkit.io/forge/internal/strs"
)

// The recommended version (design/subsystems/ways-of-working-versions.md) is
// what new projects get and what projects already on a pack are offered as
// the update. Publishing never moves it, a stock pack's included, so a
// what-if can judge a release's new version first; a person moves it with
// standards:manage-packs, which no agent credential carries. The one other
// move is a compatibility migration's amendment of the recommended
// publication (Amend), which carries the recommendation to the amended copy. Moving it cuts a follow version of each pack
// that includes this one without a pin. A follow version is not
// recommended, so nothing cascades until a person moves the next pointer,
// and no project moves: adopting stays a separate act.

// ErrNoPackVersion is a recommendation of a version the pack never had.
var ErrNoPackVersion = fault.New(fault.NotFound, "the pack has no such version")

// Recommend makes version the pack's recommended one, with its receipt,
// and cuts follow versions of the packs that include it. Recommending the
// version already recommended changes nothing.
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

// recommendIn recommends at the operation's time now, which the follow
// versions it cuts and their receipts carry too.
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
	if err := audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "pack.recommended", name, receipt); err != nil {
		return err
	}
	return s.followIn(ctx, tx, now, name, version, who)
}

// followIn cuts, for each pack whose latest version includes name without a
// pin, a version resolved against name@version. One that cannot be
// resolved is not cut; its pack.follow-refused receipt says why.
func (s *Packs) followIn(ctx context.Context, tx *database.Tx, now time.Time, name string, version int64, who string) error {
	q := wowsql.New(tx.SQL())
	includers, err := q.PacksIncluding(ctx, name)
	if err != nil {
		return err
	}
	next, err := q.PackPublication(ctx, wowsql.PackPublicationParams{Name: name, Version: version})
	if err != nil {
		return err
	}
	for _, r := range includers {
		decl, err := decodeSpec(string(r.SpecJson))
		if err != nil {
			return err
		}
		if _, pinned, err := IncludeRef(decl.Includes); err != nil || pinned != 0 {
			continue
		}
		latest, err := q.PackPublication(ctx, wowsql.PackPublicationParams{Name: r.Name, Version: r.LatestVersion})
		if err != nil {
			return err
		}
		if latest.IncludedPack != name || latest.IncludedVersion == version {
			continue
		}
		before, err := q.PackPublication(ctx, wowsql.PackPublicationParams{Name: name, Version: latest.IncludedVersion})
		if err != nil {
			return err
		}
		note := followNote(name, version, decl, before, next)
		cut, err := s.publishIn(ctx, tx, now, r.Name, r.Description, string(r.SpecJson), r.Origin,
			cut{includedVersion: version, name: fmt.Sprintf("follows %s v%d", name, version), note: note})
		if errors.Is(err, ErrInclusion) {
			if err := audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "pack.follow-refused", r.Name, map[string]any{"includes": name, "version": version, "reason": err.Error(), "packVersion": r.LatestVersion}); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if cut != r.LatestVersion {
			if err := audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "pack.followed", r.Name, map[string]any{"version": cut, "includes": name, "includedVersion": version}); err != nil {
				return err
			}
		}
	}
	return nil
}

// followNote says why a follow version exists and names each item this
// pack replaces or drops that the included pack changed or removed.
func followNote(included string, version int64, decl PackSpec, before, after wowsql.PackPublicationRow) string {
	was, now := publicationItems(before), publicationItems(after)
	var parts []string
	verb := func(item string) string {
		if _, ok := decl.Replaces[item]; ok {
			return "replace"
		}
		return "drop"
	}
	items := sortedKeys(decl.Replaces)
	for _, d := range decl.Drops {
		if !slices.Contains(items, d.Item) {
			items = append(items, d.Item)
		}
	}
	for _, item := range items {
		old, had := was[item]
		cur, has := now[item]
		switch {
		case had && !has:
			parts = append(parts, fmt.Sprintf("%s v%d no longer has %s, which you %s", included, version, item, verb(item)))
		case has && old != cur:
			parts = append(parts, fmt.Sprintf("%s v%d changed %s, which you %s", included, version, item, verb(item)))
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "; ")
	}
	return fmt.Sprintf("follows %s v%d", included, version)
}

// publicationItems is each named item of a publication with what makes it
// that item: a block's or workflow's pin, a policy's declaration.
func publicationItems(p wowsql.PackPublicationRow) map[string]string {
	out := map[string]string{}
	spec, _ := decodeSpec(string(p.SpecJson))
	var pins PinSet
	_ = json.Unmarshal(p.PinsJson, &pins)
	for _, g := range spec.Guidance {
		out[g] = fmt.Sprintf("block@%d", pins.Blocks[g])
	}
	for _, w := range spec.Workflows {
		out[w] = fmt.Sprintf("workflow@%d", pins.Workflows[w])
	}
	for _, pol := range spec.Policies {
		if text, err := strs.JSON(pol); err != nil {
			out[pol.Name] = "policy: cannot be shown (" + err.Error() + ")"
		} else {
			out[pol.Name] = "policy:" + text
		}
	}
	return out
}

// PackHistory is one pack as its page shows it: the author's declaration,
// every version with its lineage, which version is recommended, and why
// the last follow was refused when no version has been made since.
type PackHistory struct {
	Name          string        `json:"name"`
	Description   string        `json:"description"`
	Origin        string        `json:"origin"`
	Declaration   PackSpec      `json:"declaration"`
	Recommended   int64         `json:"recommended"`
	Latest        int64         `json:"latest"`
	Versions      []Publication `json:"versions"`
	FollowRefused string        `json:"followRefused,omitempty"`
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
	out := &PackHistory{Name: p.Name, Description: p.Description, Origin: p.Origin, Declaration: decl,
		Recommended: p.RecommendedVersion, Latest: p.LatestVersion, Versions: versions}
	refusals, err := audit.Recent(ctx, s.db, audit.Filter{KindPrefix: "pack.follow-refused", Subject: name, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(refusals) == 1 {
		// A refusal stands while the pack is still at the version it was
		// refused at: the next publication settles or supersedes it.
		var why struct {
			Reason      string `json:"reason"`
			PackVersion int64  `json:"packVersion"`
		}
		_ = json.Unmarshal(refusals[0].Payload, &why)
		if why.PackVersion == p.LatestVersion {
			out.FollowRefused = why.Reason
		}
	}
	return out, nil
}
