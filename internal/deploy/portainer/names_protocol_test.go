package portainer

import (
	"testing"
)

func TestPortainerStackName(t *testing.T) {
	t.Parallel()
	if got := StackName("letterbox"); got != "forge-letterbox" {
		t.Fatalf("stack name %q", got)
	}
}
