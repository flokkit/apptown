package wow

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/wow/internal/wowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/strs"
)

// Stock is what the store holds for a name: its canonical content and
// whether it is stock. Absent when there is no row.
type Stock struct {
	Content string
	IsStock bool
}

// GuidanceKey is the canonical content a block is compared on across
// releases: the body without trailing newlines and the re-serialised
// checks ("[]" reads as none).
func GuidanceKey(body, checksJSON string) string {
	checks := canonJSON(checksJSON)
	if checks == "[]" || strings.TrimSpace(checksJSON) == "" {
		checks = ""
	}
	return strings.TrimRight(body, "\n") + "\x1f" + checks
}

// StockBlock is a shipped guidance block's parts.
type StockBlock struct{ Name, Tags, ChecksJSON, Body, Tier, Conflicts string }

func (s *Library) StockCurrent(ctx context.Context, name string) (*Stock, error) {
	q := wowsql.New(s.db.SQL())
	b, err := q.BlockByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v, err := q.BlockVersion(ctx, wowsql.BlockVersionParams{BlockID: b.ID, Version: b.LatestVersion})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return &Stock{Content: GuidanceKey(v.BodyMd, string(v.ChecksJson)), IsStock: b.Origin == "stock"}, nil
}

// StockInsert seeds a stock block at version 1 with its receipt.
func (s *Library) StockInsert(ctx context.Context, b StockBlock) error {
	now := s.now().UTC()
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		q := wowsql.New(tx.SQL())
		r, err := q.InsertBlock(ctx, wowsql.InsertBlockParams{ID: ids.New(now), Name: b.Name, Tags: NormalizeTags(b.Tags), Origin: "stock", At: now, Tier: b.Tier, Conflicts: NormalizeTags(b.Conflicts)})
		if err != nil {
			return err
		}
		if _, err := q.InsertBlockVersion(ctx, wowsql.InsertBlockVersionParams{ID: ids.New(now), BlockID: r.ID, Version: 1, Body: b.Body, Checks: []byte(jsonOr(b.ChecksJSON)), At: now, Source: s.stock.Source, Signer: s.stock.Signer}); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, "apptown-system", ids.Nil, "guidance.stock.seeded", b.Name, nil)
	})
}

// StockUpdate cuts the next version of a stock block with its receipt and
// reports how many projects stay on earlier versions: a release cuts the
// version and stops; moving a project is an adoption a person decides.
func (s *Library) StockUpdate(ctx context.Context, b StockBlock) (version int, err error) {
	err = s.db.InTx(ctx, func(tx *database.Tx) error { _, v, e := s.StockUpdateIn(ctx, tx, b); version = v; return e })
	return version, err
}

func (s *Library) StockUpdateIn(ctx context.Context, tx *database.Tx, b StockBlock) (blockID ids.ID, version int, err error) {
	now := s.now().UTC()
	err = func() error {
		q := wowsql.New(tx.SQL())
		r, err := q.BlockByNameForUpdate(ctx, b.Name)
		if err != nil {
			return err
		}
		blockID = r.ID
		version = int(r.LatestVersion) + 1
		if _, err := q.InsertBlockVersion(ctx, wowsql.InsertBlockVersionParams{ID: ids.New(now), BlockID: r.ID, Version: int64(version), Body: b.Body, Checks: []byte(jsonOr(b.ChecksJSON)), At: now, Source: s.stock.Source, Signer: s.stock.Signer}); err != nil {
			return err
		}
		if _, err := q.AdvanceStockBlockVersion(ctx, wowsql.AdvanceStockBlockVersionParams{ID: r.ID, Version: int64(version), Tags: NormalizeTags(b.Tags), Tier: b.Tier, Conflicts: NormalizeTags(b.Conflicts), At: now}); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, "apptown-system", ids.Nil, "guidance.stock.upgraded", b.Name, map[string]any{"version": version})
	}()
	return blockID, version, err
}

// StockTouch refreshes what is not content when the content is unchanged.
func (s *Library) StockTouch(ctx context.Context, b StockBlock) error {
	now := s.now().UTC()
	_, err := wowsql.New(s.db.SQL()).RefreshStockBlockWords(ctx, wowsql.RefreshStockBlockWordsParams{Name: b.Name, Tags: NormalizeTags(b.Tags), Tier: b.Tier, Conflicts: NormalizeTags(b.Conflicts), At: now})
	return err
}

func jsonOr(s string) string {
	if strings.TrimSpace(s) == "" {
		return "[]"
	}
	return s
}

func (s *Workflows) StockCurrent(ctx context.Context, name string) (*Stock, error) {
	q := wowsql.New(s.db.SQL())
	r, err := q.WorkflowByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v, err := q.WorkflowVersion(ctx, wowsql.WorkflowVersionParams{Name: name, Version: r.LatestVersion})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return &Stock{Content: v.Yaml, IsStock: r.Origin == "stock"}, nil
}

func (s *Workflows) StockInsert(ctx context.Context, name, description, yamlSrc string) error {
	now := s.now().UTC()
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		q := wowsql.New(tx.SQL())
		r, err := q.InsertWorkflow(ctx, wowsql.InsertWorkflowParams{ID: ids.New(now), Name: name, Description: description, Origin: "stock", At: now})
		if err != nil {
			return err
		}
		_, err = q.InsertWorkflowVersion(ctx, wowsql.InsertWorkflowVersionParams{ID: ids.New(now), LibID: r.ID, Version: 1, Yaml: yamlSrc, At: now, Source: s.stock.Source, Signer: s.stock.Signer})
		return err
	})
}

// StockUpdate cuts the next version of a stock workflow with its receipt.
func (s *Workflows) StockUpdate(ctx context.Context, name, description, yamlSrc string) (version int, err error) {
	now := s.now().UTC()
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		q := wowsql.New(tx.SQL())
		r, err := q.WorkflowByNameForUpdate(ctx, name)
		if err != nil {
			return err
		}
		rows, err := q.WorkflowVersions(ctx, r.ID)
		if err != nil {
			return err
		}
		version = nextVersion(rows)
		if _, err := q.InsertWorkflowVersion(ctx, wowsql.InsertWorkflowVersionParams{ID: ids.New(now), LibID: r.ID, Version: int64(version), Yaml: yamlSrc, At: now, Source: s.stock.Source, Signer: s.stock.Signer}); err != nil {
			return err
		}
		if _, err := q.AdvanceWorkflowVersion(ctx, wowsql.AdvanceWorkflowVersionParams{ID: r.ID, Version: int64(version), Description: description, At: now}); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, "apptown-system", ids.Nil, "library.workflow.updated", name, map[string]any{"version": version})
	})
	return version, err
}

func (s *Workflows) StockTouch(ctx context.Context, name, description string) error {
	_, err := wowsql.New(s.db.SQL()).RefreshWorkflowWords(ctx, wowsql.RefreshWorkflowWordsParams{Name: name, Description: description})
	return err
}

// PolicyKey is the canonical content of a library policy: the rule and its
// parameters. The description is words about it, refreshed separately.
func PolicyKey(rule, configJSON string) string { return rule + "\x1f" + canonJSON(configJSON) }

func (s *Policies) StockCurrent(ctx context.Context, name string) (*Stock, error) {
	r, err := wowsql.New(s.db.SQL()).LibraryPolicyByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &Stock{Content: PolicyKey(r.Rule, string(r.ConfigJson)), IsStock: r.Origin == "stock"}, nil
}

func (s *Policies) StockInsert(ctx context.Context, lp LibraryPolicy) error {
	now := s.now().UTC()
	config, err := strs.JSON(lp.With)
	if err != nil {
		return err
	}
	_, err = wowsql.New(s.db.SQL()).InsertLibraryPolicy(ctx, wowsql.InsertLibraryPolicyParams{ID: ids.New(now), Name: lp.Name, Description: lp.Description, Rule: lp.Rule, Config: []byte(config), Origin: "stock", At: now})
	return err
}

func (s *Policies) StockUpdate(ctx context.Context, lp LibraryPolicy) error {
	now := s.now().UTC()
	config, err := strs.JSON(lp.With)
	if err != nil {
		return err
	}
	_, err = wowsql.New(s.db.SQL()).EditLibraryPolicy(ctx, wowsql.EditLibraryPolicyParams{Name: lp.Name, Description: lp.Description, Rule: lp.Rule, Config: []byte(config), Origin: "stock", At: now})
	return err
}

func (s *Policies) StockTouch(ctx context.Context, lp LibraryPolicy) error {
	_, err := wowsql.New(s.db.SQL()).RefreshStockPolicyWords(ctx, wowsql.RefreshStockPolicyWordsParams{Name: lp.Name, Description: lp.Description})
	return err
}
