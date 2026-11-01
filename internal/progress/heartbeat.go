package progress

import (
	"fmt"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

// Heartbeat opens a live feed for a long, output-silent step and ticks
// an elapsed line every few seconds, so a deploy or scan that prints nothing
// for 40s stops looking hung (complaint #3). The feed also carries elapsed
// time to the UI header and `apptown watch`; it vanishes when the returned stop
// func runs — ephemeral sign-of-life, never part of the ledger. Safe to stop
// more than once.
func (f *Feeds) Heartbeat(stepID ids.ID, phase string) func() {
	f.Start(stepID)
	done := make(chan struct{})
	go func() {
		start := f.now()
		t := time.NewTicker(8 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				f.Append(stepID, fmt.Sprintf("%s… %ds elapsed", phase, int(f.now().Sub(start).Seconds())))
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			f.discard(stepID)
		})
	}
}
