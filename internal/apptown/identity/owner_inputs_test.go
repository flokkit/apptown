package identity_test

import (
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/identity"
)

func TestSpecKeepsOwnerFactsPrivate(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(identity.Spec{})
	for _, name := range []string{"Origin"} {
		if field, ok := typ.FieldByName(name); ok && field.IsExported() {
			t.Errorf("outside caller can set %s.%s", typ.Name(), name)
		}
	}
}
