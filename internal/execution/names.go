package execution

import (
	"crypto/rand"
	"encoding/hex"
)

// UniqueName is prefix with a random suffix: a container name no other run
// shares, even one started in the same instant (a fleet check starts one
// per project at once). A suffix from the clock repeats; this does not.
func UniqueName(prefix string) string {
	var b [6]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
	return prefix + "-" + hex.EncodeToString(b[:])
}
