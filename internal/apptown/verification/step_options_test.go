package verification

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema/schematest"
)

// The scan and guidance runners read exactly the options a save lets a
// workflow write for their kinds, plus what AppTown records.
func TestTheCheckRunnersReadExactlyTheirKindsOptions(t *testing.T) {
	t.Parallel()
	if err := schematest.OptionsDecodedBy(ScanKind, scanConfig{}); err != nil {
		t.Error(err)
	}
	if err := schematest.OptionsDecodedBy(GuidanceKind, guidanceConfig{}); err != nil {
		t.Error(err)
	}
}
