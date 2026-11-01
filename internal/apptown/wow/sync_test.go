package wow

import (
	"errors"
	"strings"
	"testing"
)

// fakeStore is one kind's rows: content by name, with whether each is stock.
type fakeStore struct {
	rows     map[string][2]string // name → {content, "stock"|"custom"}
	versions map[string]int
	touched  []string
	failRead map[string]bool
	receipts []string
}

func (f *fakeStore) kind(label string, versioned bool) SyncKind {
	return SyncKind{
		Label: label, Versioned: versioned,
		receiptSeeded: label + ".seeded", ReceiptUpdated: label + ".updated",
		Current: func(name string) (string, bool, bool, error) {
			if f.failRead[name] {
				return "", false, false, errors.New("boom")
			}
			r, ok := f.rows[name]
			return r[0], r[1] == "stock", ok, nil
		},
		Insert: func(it SyncItem) error {
			f.rows[it.Name] = [2]string{it.Content, "stock"}
			f.versions[it.Name] = 1
			return nil
		},
		Update: func(it SyncItem) (int, error) {
			f.rows[it.Name] = [2]string{it.Content, "stock"}
			f.versions[it.Name]++
			return f.versions[it.Name], nil
		},
		touch: func(it SyncItem) error { f.touched = append(f.touched, it.Name); return nil },
	}
}

func (f *fakeStore) receipt(kind, name string, payload map[string]any) {
	f.receipts = append(f.receipts, kind+":"+name)
}

// The five cases of the contract, counted and receipted as the design
// says: absent is seeded, custom is kept, unchanged is touched, changed
// is versioned (with a receipt only for versioned kinds), an unreadable
// row is an error and never stops the rest.
func TestSyncContract(t *testing.T) {
	f := &fakeStore{
		rows: map[string][2]string{
			"kept":       {"theirs", "custom"},
			"same":       {"v1", "stock"},
			"moved":      {"v1", "stock"},
			"unreadable": {"x", "stock"},
		},
		versions: map[string]int{"same": 1, "moved": 1},
		failRead: map[string]bool{"unreadable": true},
	}
	items := []SyncItem{
		{Name: "new", Content: "hello"},
		{Name: "kept", Content: "ours"},
		{Name: "same", Content: "v1"},
		{Name: "moved", Content: "v2"},
		{Name: "unreadable", Content: "y"},
	}
	c := Sync(f.kind("block", true), items, f.receipt)
	if c != (SyncCounts{Kind: "block", Added: 1, Updated: 1, Unchanged: 1, Kept: 1, Errors: 1}) {
		t.Fatalf("counts: %+v", c)
	}
	if f.rows["kept"][0] != "theirs" || f.rows["new"][1] != "stock" || f.rows["moved"][0] != "v2" || f.versions["moved"] != 2 {
		t.Fatalf("rows: %+v", f.rows)
	}
	if len(f.touched) != 1 || f.touched[0] != "same" {
		t.Fatalf("touched: %v", f.touched)
	}
	if len(f.receipts) != 2 || f.receipts[0] != "block.seeded:new" || f.receipts[1] != "block.updated:moved" {
		t.Fatalf("receipts: %v", f.receipts)
	}
	if c.String() != "stock blocks: 1 added, 1 updated, 1 unchanged, 1 kept yours, 0 skipped, 1 errors" {
		t.Fatalf("line: %s", c)
	}
	// An unversioned kind overwrites in place and leaves no update receipt.
	g := &fakeStore{rows: map[string][2]string{"p": {"old", "stock"}}, versions: map[string]int{"p": 1}}
	c = Sync(g.kind("pack", false), []SyncItem{{Name: "p", Content: "new"}}, g.receipt)
	if c.Updated != 1 || len(g.receipts) != 0 {
		t.Fatalf("unversioned: %+v %v", c, g.receipts)
	}
	// No receipt writer is fine.
	if c := Sync(g.kind("pack", true), []SyncItem{{Name: "q", Content: "n"}}, nil); c.Added != 1 {
		t.Fatalf("nil receipt: %+v", c)
	}
}

// The boot log names each kind of stock content in the plural, as a person
// writes it: "stock policies", never "stock policys".
//
// Kills: the plural back to label + "s".
func TestTheStockSyncLineNamesTheKindInThePlural(t *testing.T) {
	t.Parallel()
	for label, want := range map[string]string{"policy": "stock policies:", "workflow": "stock workflows:", "guidance block": "stock guidance blocks:"} {
		if got := (SyncCounts{Kind: label}).String(); !strings.HasPrefix(got, want) {
			t.Errorf("%s: %q, want it to start %q", label, got, want)
		}
	}
}
