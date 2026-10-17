package conversation

import (
	"context"
	"log"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/usage"
)

// HarvestUsage reads new lines of a session's records (byte offset
// tracked per file in the usage ledger) and writes aggregated ledger rows.
// It records spend already incurred, and its callers remove the records
// after it (a chat's end, a project's cleanup), so the whole harvest, its
// cursor reads included, runs though the caller stops.
func HarvestUsage(ctx context.Context, ledger *usage.Ledger, s *console.Session) {
	ctx = context.WithoutCancel(ctx)
	for _, path := range s.Records() {
		offset, err := ledger.Cursor(ctx, path)
		found, err := database.Lookup(err)
		if err != nil {
			log.Printf("read failed: %v", err)
		}
		if !found {
			continue
		}
		rd := console.Reader{Path: path, Off: offset}
		entries, err := rd.Next()
		if err != nil {
			continue
		}
		type key struct{ provider, model string }
		agg := map[key]*usage.Totals{}
		for _, e := range entries {
			if e.Role != "assistant" {
				continue
			}
			k := key{e.Provider, e.Model}
			if agg[k] == nil {
				agg[k] = &usage.Totals{}
			}
			m := e.Usage
			agg[k].Add(m.Input, m.Output, m.CacheRead, m.CacheWrite, m.Total, m.Cost.Total)
		}
		newOffset := rd.Off
		for k, u := range agg {
			if u.Total == 0 {
				continue
			}
			if err := ledger.Record(ctx, usage.Entry{Kind: "console", ProjectID: Of(s).ProjectID, SessionID: s.ID, Actor: s.Actor, Who: spender(s), Provider: k.provider, Model: k.model}, *u, ""); err != nil {
				log.Printf("recording console usage for %s: %v", s.ID, err)
			}
		}
		if newOffset != offset {
			if err := ledger.SetCursor(ctx, path, newOffset); err != nil {
				log.Printf("recording the usage cursor for %s: %v", path, err)
			}
		}
	}
}

// spender is who a chat's spend is recorded against: its opener's principal
// as the chat recorded it (none when unrecorded) and the chat.
func spender(s *console.Session) access.Actor {
	a := Principal(s).Actor()
	a.Name = s.Actor
	return a
}
