package deployment

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// What a target can do decides what is asked of it: capabilities that
// cannot be read are an error, never a target that claims none.
func TestATargetWhoseCapabilitiesCannotBeReadIsAnError(t *testing.T) {
	t.Parallel()
	row := deploymentsql.Target{ID: idstest.N(3), Name: "far", Kind: KindDockerHost, Status: "ready", CapabilitiesJson: []byte(`["not","an","object"]`)}
	if _, err := targetRecord(row, nil); err == nil || !strings.Contains(err.Error(), "capabilities could not be read") {
		t.Fatalf("unreadable capabilities: %v", err)
	}
	row.CapabilitiesJson = []byte(`{}`)
	if _, err := targetRecord(row, nil); err != nil {
		t.Fatalf("readable capabilities: %v", err)
	}
}
