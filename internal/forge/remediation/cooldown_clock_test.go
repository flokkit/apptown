package remediation

import (
	"sync"
	"testing"
	"time"
)

// The pool's cooldown after a provider refuses runs on AppTown's clock:
// held now, it is still cooling two minutes on and has ended four minutes
// on, with that clock an hour ahead of the real one.
//
// Kills: the cooldown judged by the real clock (an hour behind its end, it
// never ends).
func TestThePoolsCooldownEndsByTheForgesClock(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	at := time.Now().Add(time.Hour)
	w := &cooldown{clock: func() time.Time { mu.Lock(); defer mu.Unlock(); return at }}
	move := func(d time.Duration) { mu.Lock(); at = at.Add(d); mu.Unlock() }
	w.hold("the provider refused")
	move(2 * time.Minute)
	if !w.cooling() {
		t.Fatal("the cooldown ended within its three minutes")
	}
	move(2 * time.Minute)
	if w.cooling() {
		t.Fatal("the cooldown lasted past its three minutes")
	}
}
