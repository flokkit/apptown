package workflow

import (
	"encoding/json"
	"strings"
)

// AcceptanceScope is the check a remediation change was opened to close,
// stored in acceptance_json. An uplift's
// acceptance checks join it through absoluteChecksOf.
type AcceptanceScope struct {
	set map[string]bool // "block|check" judged absolutely
}

func (t AcceptanceScope) Covers(fingerprint string) bool {
	for k := range t.set {
		parts := strings.SplitN(k, "|", 2)
		if len(parts) == 2 && strings.Contains(fingerprint, "|"+parts[0]+"@") && strings.Contains(fingerprint, "|"+parts[1]+"|") {
			return true
		}
	}
	return false
}

func AcceptanceScopeOf(ch *Change) AcceptanceScope {
	t := AcceptanceScope{set: map[string]bool{}}
	if ch == nil {
		return t
	}
	if ch.AcceptanceJSON != "" && ch.AcceptanceJSON != "{}" {
		var acc Acceptance
		if json.Unmarshal([]byte(ch.AcceptanceJSON), &acc) == nil {
			for _, c := range acc.Checks {
				t.set[c.Block+"|"+c.Check] = true
			}
		}
	}

	return t
}

func (t AcceptanceScope) Keys() []string {
	out := make([]string, 0, len(t.set))
	for k := range t.set {
		out = append(out, k)
	}
	return out
}
