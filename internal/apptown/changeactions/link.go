package changeactions

import (
	"fmt"

	"github.com/flokkit/apptown/internal/ids"
)

// URL: the human-facing page for a change, when the instance knows
// its public host ("" otherwise: callers omit the link rather than fake one).
func URL(publicHost string, id ids.ID) string {
	if publicHost == "" {
		return ""
	}
	return fmt.Sprintf("https://%s/changes/%d", publicHost, id)
}
