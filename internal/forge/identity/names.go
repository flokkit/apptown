package identity

import (
	"fmt"
	"strings"
	"unicode"

	"flokkit.io/forge/internal/fault"
)

// ValidName is the one check of a person's name at every door that records
// or names one: a trusted header, an open-mode actor, a member, a grant's
// person, a token's holder, the startup administrator seed. Actor strings
// are parsed across the forge ("alice via console/<s>", "chat/<s>",
// "run/<id>"), so a person's name may not take one of those forms, nor be
// one of the forge's own names (SystemActor), nor carry text that does not
// survive display. Forms that the stricter account and group rules already
// refuse pass through here too.
func ValidName(name string) error {
	reason := ""
	switch {
	case name == "":
		reason = "it is empty"
	case strings.TrimSpace(name) != name:
		reason = "it starts or ends with a space"
	case strings.IndexFunc(name, unicode.IsControl) >= 0:
		reason = "it contains a control character"
	case strings.Contains(name, "/"):
		reason = `it contains "/", which names a chat, a run or a connection`
	case strings.Contains(name, ":"):
		reason = `it contains ":", which names how someone acted (token-cli:<name>, chat:<id>)`
	case strings.Contains(name, " via "):
		reason = `it contains " via ", which names someone acting through something`
	case SystemActor(name):
		reason = "it is one of AppTown's own names"
	default:
		return nil
	}
	return fault.New(fault.Invalid, fmt.Sprintf("%q cannot be a person's name: %s", name, reason))
}
