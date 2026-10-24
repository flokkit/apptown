package wow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/strs"
)

// The release's ways of working (stock/) sync into the store at boot under
// the one contract in sync.go: workflows, guidance blocks, policies, packs.
// The files come from Dependencies.Stock; what a shipped block's checks
// mean is conformance's (Dependencies.Checks); who imports a block is
// adoption's (Dependencies.Importers). Both are observed, never owned here.

// SyncStock puts everything the release ships into the store.
func (s *Service) SyncStock(ctx context.Context) {
	s.SyncStockWorkflows(ctx)
	s.SyncStockGuidance(ctx)
	s.SyncStockPolicies(ctx)
	s.SyncStockPacks(ctx)
}

// ---- workflows ---------------------------------------------------------------

// notWorkflowFiles are the top-level files that are other kinds' input: the
// update's header, packs, policies, scanners, the catalog and the tool
// versions list.
var notWorkflowFiles = map[string]bool{"update.yaml": true, "packs.yaml": true, "policies.yaml": true, "scanners.yaml": true, "catalog.yaml": true, "tool-versions.yaml": true}

// readStockWorkflows reads the workflow files at the top of stock/.
func readStockWorkflows(fsys fs.FS) (items []SyncItem, skipped int) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		log.Printf("stock workflows: %v", err)
		return nil, 0
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".yaml") || notWorkflowFiles[n] {
			continue
		}
		raw, err := fs.ReadFile(fsys, n)
		if err != nil {
			log.Printf("stock workflow %s: %v (skipped)", n, err)
			skipped++
			continue
		}
		doc, err := schema.ParseWorkflow(raw)
		if err != nil {
			// A bad vendor file never breaks startup — it is named and skipped.
			log.Printf("stock workflow %s: %v (skipped)", n, err)
			skipped++
			continue
		}
		items = append(items, SyncItem{Name: doc.Metadata.Name, Content: string(raw), Data: doc.Metadata.Description})
	}
	return items, skipped
}

// SyncStockWorkflows puts the shipped workflows into the library: a new
// library version only when the YAML changed (versioned; the version's
// transaction writes library.workflow.updated).
func (s *Service) SyncStockWorkflows(ctx context.Context) SyncCounts {
	items, skipped := readStockWorkflows(s.deps.Stock)
	kind := SyncKind{
		Label: "workflow", Versioned: true,
		Current: func(name string) (string, bool, bool, error) {
			st, err := s.workflows.StockCurrent(ctx, name)
			if err != nil || st == nil {
				return "", false, false, err
			}
			return st.Content, st.IsStock, true, nil
		},
		Insert: func(it SyncItem) error { return s.workflows.StockInsert(ctx, it.Name, it.Data.(string), it.Content) },
		Update: func(it SyncItem) (int, error) {
			return s.workflows.StockUpdate(ctx, it.Name, it.Data.(string), it.Content)
		},
		Touch: func(it SyncItem) error { return s.workflows.StockTouch(ctx, it.Name, it.Data.(string)) },
	}
	c := Sync(kind, items, nil)
	c.Skipped += skipped
	log.Print(c)
	return c
}

// ---- guidance blocks ---------------------------------------------------------
//
// AppTown SHIPS guidance blocks in stock/guidance/**/*.md;
// they sync as origin='stock'. A release that changes a block's body or
// checks cuts the next version and stops there: projects importing the
// block stay on the version they adopted (ways-of-working-versions.md §9
// step 1, ruling 1). Institution-authored blocks (origin='') are untouched.

// ParseStockGuidance reads a shipped block: front matter (name, tags, an
// optional checks JSON array on one line, tier, conflicts; any other key, or
// a line that is not key: value, is refused) then the markdown body. The tier is "platform" (AppTown's own contract, on
// every repo-backed project) or absent.
func ParseStockGuidance(raw string) (StockBlock, error) {
	var b StockBlock
	if !strings.HasPrefix(raw, "---\n") {
		return b, fmt.Errorf("missing frontmatter")
	}
	rest := raw[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return b, fmt.Errorf("unterminated frontmatter")
	}
	for i, line := range strings.Split(rest[:end], "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return b, fmt.Errorf("frontmatter line %d is not key: value", i+2)
		}
		value = strings.TrimSpace(value)
		switch key {
		case "name":
			b.Name = value
		case "tags":
			b.Tags = NormalizeTags(value)
		case "checks":
			b.ChecksJSON = value
		case "tier":
			b.Tier = value
		case "conflicts":
			b.Conflicts = NormalizeTags(value)
		case "apiVersion":
			if err := schema.CheckAPIVersion(value); err != nil {
				return b, fmt.Errorf("frontmatter line %d: %w", i+2, err)
			}
		default:
			return b, fmt.Errorf("frontmatter line %d: unknown key %q (known: apiVersion, name, tags, checks, tier, conflicts)", i+2, key)
		}
	}
	b.Body = strings.TrimLeft(rest[end+len("\n---\n"):], "\n")
	if b.Name == "" {
		return b, fmt.Errorf("frontmatter missing name")
	}
	if b.Tier != "" && b.Tier != TierPlatform.String() {
		return b, fmt.Errorf("frontmatter tier must be platform or absent, got %q", b.Tier)
	}
	return b, nil
}

// ReadStockGuidance walks the shipped stock/guidance/**/*.md. A block's
// checks are read by conformance (checks): re-serialised so the stored and
// compared form is canonical, or dropped when bad — a bad checks payload
// never breaks startup, the body is kept. nil checks keeps them as written.
func ReadStockGuidance(fsys fs.FS, checks ChecksReader) (blocks []StockBlock, skipped int) {
	_ = fs.WalkDir(fsys, "guidance", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		raw, rerr := fs.ReadFile(fsys, path)
		if rerr != nil {
			log.Printf("stock guidance %s: %v (skipped)", path, rerr)
			skipped++
			return nil
		}
		b, perr := ParseStockGuidance(string(raw))
		if perr != nil {
			log.Printf("stock guidance %s: %v (skipped)", path, perr)
			skipped++
			return nil
		}
		if b.ChecksJSON != "" && checks != nil {
			canonical, err := checks.Canonical(b.ChecksJSON)
			if err != nil {
				log.Printf("stock guidance %s: bad checks (dropped): %v", b.Name, err)
				canonical = ""
			}
			b.ChecksJSON = canonical
		}
		blocks = append(blocks, b)
		return nil
	})
	return blocks, skipped
}

// SyncStockGuidance puts the shipped blocks in the store: a new version
// when a block's body or checks changed (the version's transaction writes
// guidance.stock.seeded / .upgraded). The platform tier no longer appends
// to existing projects at boot: a new platform block reaches a project
// through adoption.
func (s *Service) SyncStockGuidance(ctx context.Context) SyncCounts {
	blocks, skipped := ReadStockGuidance(s.deps.Stock, s.deps.Checks)
	c := s.syncGuidance(ctx, blocks)
	c.Skipped += skipped
	log.Print(c)
	return c
}

func (s *Service) syncGuidance(ctx context.Context, blocks []StockBlock) SyncCounts {
	items := make([]SyncItem, 0, len(blocks))
	for _, b := range blocks {
		items = append(items, SyncItem{Name: b.Name, Content: GuidanceKey(b.Body, b.ChecksJSON), Data: b})
	}
	block := func(it SyncItem) StockBlock { return it.Data.(StockBlock) }
	kind := SyncKind{
		Label: "guidance block", Versioned: true,
		Current: func(name string) (string, bool, bool, error) {
			st, err := s.library.StockCurrent(ctx, name)
			if err != nil || st == nil {
				return "", false, false, err
			}
			return st.Content, st.IsStock, true, nil
		},
		Insert: func(it SyncItem) error { return s.library.StockInsert(ctx, block(it)) },
		Update: func(it SyncItem) (int, error) {
			var blockID ids.ID
			var next int
			err := s.db.InTx(ctx, func(tx *database.Tx) (err error) {
				blockID, next, err = s.library.StockUpdateIn(ctx, tx, block(it))
				return err
			})
			if err != nil {
				return 0, err
			}
			// Frozen: a release cuts the new version and stops; projects
			// importing the block stay on the version they adopted. The
			// count keeps the release honest about it.
			if s.deps.Importers != nil {
				if behind, err := s.deps.Importers.Behind(ctx, blockID, next); err == nil && behind > 0 {
					log.Printf("stock guidance block %q: %d project(s) stay on earlier versions (adoption is frozen)", it.Name, behind)
				}
			}
			return next, nil
		},
		Touch: func(it SyncItem) error { return s.library.StockTouch(ctx, block(it)) },
	}
	return Sync(kind, items, nil)
}

// ---- policies ----------------------------------------------------------------

// SyncStockPolicies puts the shipped policies.yaml in the library as stock
// definitions: overwritten in place (unversioned); a policy authored here
// under a stock name is kept and the collision logged.
func (s *Service) SyncStockPolicies(ctx context.Context) SyncCounts {
	c := SyncCounts{Kind: "policy"}
	policies, err := ReadStockPolicies(s.deps.Stock)
	if err != nil {
		log.Printf("stock %v", err)
		c.Errors++
		log.Print(c)
		return c
	}
	var items []SyncItem
	skipped := 0
	for _, lp := range policies {
		config, err := strs.JSON(lp.With)
		if err != nil {
			log.Printf("stock policy %q: %v (skipped)", lp.Name, err)
			skipped++
			continue
		}
		items = append(items, SyncItem{Name: lp.Name, Content: PolicyKey(lp.Rule, config), Data: lp})
	}
	kind := SyncKind{
		Label: "policy",
		Current: func(name string) (string, bool, bool, error) {
			st, err := s.policies.StockCurrent(ctx, name)
			if err != nil || st == nil {
				return "", false, false, err
			}
			return st.Content, st.IsStock, true, nil
		},
		Insert: func(it SyncItem) error { return s.policies.StockInsert(ctx, it.Data.(LibraryPolicy)) },
		Update: func(it SyncItem) (int, error) { return 0, s.policies.StockUpdate(ctx, it.Data.(LibraryPolicy)) },
		Touch:  func(it SyncItem) error { return s.policies.StockTouch(ctx, it.Data.(LibraryPolicy)) },
	}
	c = Sync(kind, items, nil)
	c.Skipped += skipped
	log.Print(c)
	return c
}

// ---- packs -------------------------------------------------------------------

// SyncStockPacks puts the shipped packs.yaml in the store as stock packs,
// published as immutable versions (stock packs are read-only; clone to
// customise). A pack authored here under a stock name is kept and the
// collision logged. Contents that moved since the last publication cut
// the packs that carry them.
func (s *Service) SyncStockPacks(ctx context.Context) SyncCounts {
	c := s.syncPacks(ctx)
	s.withdrawUnshippedPacks(ctx)
	if _, err := s.packs.CutOnPinChange(ctx); err != nil {
		log.Printf("packs: cutting on moved contents: %v", err)
	}
	for _, line := range s.waitingStockVersions(ctx) {
		log.Print(line)
	}
	return c
}

// waitingStockVersions names each stock pack whose latest version is not
// the recommended one: a release publishes, and a person recommends after
// a what-if. The boot log says what waits and the command that moves it.
func (s *Service) waitingStockVersions(ctx context.Context) []string {
	packs, err := s.packs.Published(ctx)
	if err != nil {
		return []string{fmt.Sprintf("stock packs: reading which versions wait for a recommendation: %v", err)}
	}
	var out []string
	for _, p := range packs {
		if p.Origin == "stock" && p.LatestVersion != p.RecommendedVersion {
			out = append(out, fmt.Sprintf("stock pack %s: v%d published, v%d recommended (apptown pack recommend %s %d)", p.Name, p.LatestVersion, p.RecommendedVersion, p.Name, p.LatestVersion))
		}
	}
	return out
}

func (s *Service) syncPacks(ctx context.Context) SyncCounts {
	packs, err := ReadStockPacks(s.deps.Stock)
	if err != nil {
		log.Printf("stock packs.yaml: %v", err)
	}
	var items []SyncItem
	failed := 0
	for _, pk := range packs {
		spec, err := strs.JSON(pk.Spec())
		if err != nil {
			log.Printf("stock pack %q: %v (skipped)", pk.Name, err)
			failed++
			continue
		}
		items = append(items, SyncItem{Name: pk.Name, Data: pk.Description, Content: canonJSON(spec)})
	}
	kind := SyncKind{
		Label: "pack",
		Current: func(name string) (string, bool, bool, error) {
			pk, err := s.packs.ByName(ctx, name)
			if errors.Is(err, sql.ErrNoRows) {
				return "", false, false, nil
			} else if err != nil {
				return "", false, false, err
			}
			return canonJSON(pk.SpecJSON), pk.Origin == "stock", true, nil
		},
		Insert: func(it SyncItem) error {
			_, err := s.packs.Publish(ctx, it.Name, it.Data.(string), it.Content, "stock")
			return err
		},
		Update: func(it SyncItem) (int, error) {
			return s.packs.Publish(ctx, it.Name, it.Data.(string), it.Content, "stock")
		},
		Touch: func(it SyncItem) error { return s.packs.Describe(ctx, it.Name, it.Data.(string)) },
	}
	c := Sync(kind, items, nil)
	c.Errors += failed
	log.Print(c)
	return c
}
