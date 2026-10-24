package conversation

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/console"
)

// HarvestUsage reads new lines of a session's records (byte offset
// tracked per file in the usage ledger) and writes aggregated ledger rows.
// It records spend already incurred, so the whole harvest, its cursor reads
// included, runs though the caller stops. Each file's rows and its cursor are
// one write ([usage.Ledger.RecordHarvest]): the cursor never passes spend
// that was not stored. A record list, cursor or record that cannot be read,
// or a write that fails, is an error: the chat's end that settles with it
// then keeps the records.
func HarvestUsage(ctx context.Context, ledger *usage.Ledger, s *console.Session) error {
	ctx = context.WithoutCancel(ctx)
	paths, err := s.Records()
	if err != nil {
		return fmt.Errorf("listing chat %s's records: %w", s.ID, err)
	}
	for _, path := range paths {
		offset, err := ledger.Cursor(ctx, path)
		if err != nil {
			return fmt.Errorf("reading the usage cursor of %s: %w", path, err)
		}
		rd := console.Reader{Path: path, Off: offset}
		entries, err := rd.Next()
		if err != nil {
			return fmt.Errorf("reading chat %s's record %s: %w", s.ID, path, err)
		}
		if rd.Off == offset {
			continue
		}
		type key struct{ provider, model string }
		agg := map[key]*usage.Totals{}
		var order []key
		for _, e := range entries {
			if e.Role != "assistant" {
				continue
			}
			k := key{e.Provider, e.Model}
			if agg[k] == nil {
				agg[k] = &usage.Totals{}
				order = append(order, k)
			}
			m := e.Usage
			agg[k].Add(m.Input, m.Output, m.CacheRead, m.CacheWrite, m.Total, m.Cost.Total)
		}
		var rows []usage.Harvested
		for _, k := range order {
			if u := agg[k]; u.Total != 0 {
				rows = append(rows, usage.Harvested{Entry: usage.Entry{Kind: "console", ProjectID: Of(s).ProjectID, SessionID: s.ID, Actor: s.Actor, Who: spender(s), Provider: k.provider, Model: k.model}, Totals: *u})
			}
		}
		if err := ledger.RecordHarvest(ctx, path, rd.Off, rows); err != nil {
			return fmt.Errorf("recording chat %s's usage: %w", s.ID, err)
		}
	}
	return nil
}

// spender is who a chat's spend is recorded against: its opener's principal
// as the chat recorded it (none when unrecorded) and the chat.
func spender(s *console.Session) access.Actor {
	a := Principal(s).Actor()
	a.Name = s.Actor
	return a
}
