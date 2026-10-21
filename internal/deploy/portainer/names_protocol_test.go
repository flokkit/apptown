package portainer

import (
	"testing"
)

// A stack keeps the prefix its deployment was made with: one made before
// 1.0 keeps forge-<slug>, a new one is apptown-<slug>.
func TestPortainerStackName(t *testing.T) {
	t.Parallel()
	for prefix, want := range map[string]string{"apptown-": "apptown-letterbox", "forge-": "forge-letterbox"} {
		if got := StackName(prefix, "letterbox"); got != want {
			t.Errorf("stack name under %s: %q, want %q", prefix, got, want)
		}
	}
}
