package wow

import (
	"fmt"
	"log"
)

// Stock sync: five kinds of thing
// ship inside the binary (workflows, guidance blocks, packs, policies,
// integration seeds) and one procedure puts each into the store at boot.
// The contract:
//
//   - an item's canonical content is compared with the stored row's; the
//     kind says what canonical means (guidance re-serialises its checks
//     and ignores trailing newlines, so a re-release does not bump every
//     block);
//   - absent → inserted as stock (version 1 for versioned kinds);
//   - present but not stock (authored here under a name the release now
//     ships) → kept exactly as it is, the stock one not installed, and
//     the log says so: nothing silently shadows anything;
//   - present, stock, changed → versioned kinds get a new version row,
//     the head advances and a receipt names the version; unversioned
//     kinds are overwritten in place;
//   - present, stock, unchanged → the kind's Touch refreshes what is not
//     content (a description, tags);
//   - every skip (an unreadable file, a kept custom row) and every
//     database error is logged, never swallowed; the counts come back and
//     boot prints them.

// SyncItem is one shipped thing.
type SyncItem struct {
	Name    string
	Content string // canonical form: what is stored and compared
	Data    any    // whatever the kind's writers need beyond the content
}

// SyncKind is how one kind of thing is read and written.
type SyncKind struct {
	Label string // "workflow", "guidance block", "pack", "policy", "integration"
	// Current reads the stored row: its canonical content and whether it
	// is stock. ok=false when there is no row.
	Current func(name string) (content string, stock, ok bool, err error)
	// Insert creates the stock row (version 1 for versioned kinds).
	Insert func(it SyncItem) error
	// Update replaces the content. Versioned kinds add a version row,
	// advance the head and return the new version; unversioned kinds
	// overwrite.
	Update func(it SyncItem) (version int, err error)
	// Touch refreshes what is not content when the content is unchanged.
	// Optional.
	Touch func(it SyncItem) error
	// Receipt kinds written for a seeded and for an updated row; "" = none.
	ReceiptSeeded, ReceiptUpdated string
	// Versioned says whether Update produces a new version worth a line
	// in the log and a receipt.
	Versioned bool
}

// SyncCounts is what one sync did: the boot log's line for the kind.
type SyncCounts struct {
	Kind                                             string
	Added, Updated, Unchanged, Kept, Skipped, Errors int
}

func (c SyncCounts) String() string {
	return fmt.Sprintf("stock %ss: %d added, %d updated, %d unchanged, %d kept yours, %d skipped, %d errors",
		c.Kind, c.Added, c.Updated, c.Unchanged, c.Kept, c.Skipped, c.Errors)
}

// SyncReceipt records one ledger line about a stock item; the caller supplies
// the writer so the sync stays free of the ledger's transaction shape.
type SyncReceipt func(kind, name string, payload map[string]any)

// Sync puts one kind's shipped items into the store under the contract
// above and reports what it did.
func Sync(kind SyncKind, items []SyncItem, receipt SyncReceipt) SyncCounts {
	c := SyncCounts{Kind: kind.Label}
	for _, it := range items {
		cur, isStock, ok, err := kind.Current(it.Name)
		if err != nil {
			log.Printf("stock %s %q: read: %v", kind.Label, it.Name, err)
			c.Errors++
			continue
		}
		switch {
		case !ok:
			if err := kind.Insert(it); err != nil {
				log.Printf("stock %s %q: insert: %v", kind.Label, it.Name, err)
				c.Errors++
				continue
			}
			if kind.ReceiptSeeded != "" && receipt != nil {
				receipt(kind.ReceiptSeeded, it.Name, nil)
			}
			c.Added++
		case !isStock:
			log.Printf("stock %s %q: one authored here has that name — keeping yours; the stock one is not installed", kind.Label, it.Name)
			c.Kept++
		case cur == it.Content:
			if kind.Touch != nil {
				if err := kind.Touch(it); err != nil {
					log.Printf("stock %s %q: refresh: %v", kind.Label, it.Name, err)
					c.Errors++
					continue
				}
			}
			c.Unchanged++
		default:
			v, err := kind.Update(it)
			if err != nil {
				log.Printf("stock %s %q: update: %v", kind.Label, it.Name, err)
				c.Errors++
				continue
			}
			if kind.Versioned {
				log.Printf("stock %s %q: now v%d", kind.Label, it.Name, v)
				if kind.ReceiptUpdated != "" && receipt != nil {
					receipt(kind.ReceiptUpdated, it.Name, map[string]any{"version": v})
				}
			}
			c.Updated++
		}
	}
	return c
}
