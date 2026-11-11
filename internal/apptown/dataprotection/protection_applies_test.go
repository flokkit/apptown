package dataprotection

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
)

// Protection is kept for a standing deployment on a target that holds data;
// a preview, or a target that holds none, has no backup to be unknown.
func TestProtectionAppliesToStandingDeploymentsOnTargetsThatHoldData(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{deployment.KindDockerHost, deployment.KindPortainer, deployment.KindDOApp, deployment.KindAWSECS} {
		if !Applies(deployment.Standing, kind) {
			t.Errorf("standing on %s: not applied", kind)
		}
		if Applies(deployment.Kind(0), kind) || Applies(deployment.Standing+1, kind) {
			t.Errorf("non-standing on %s: applied", kind)
		}
	}
	if Applies(deployment.Standing, deployment.KindS3Bucket) || Applies(deployment.Standing, "") {
		t.Error("a target holding no data, or none, applied")
	}
}
