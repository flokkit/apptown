package deployment_test

import (
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
)

func TestDeploySelectionKeepsOwnerFactsPrivate(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(deployment.DeploySelection{})
	for _, name := range []string{"AddressRevision", "CustomHostname"} {
		if field, ok := typ.FieldByName(name); ok && field.IsExported() {
			t.Errorf("outside caller can set %s.%s", typ.Name(), name)
		}
	}
}
