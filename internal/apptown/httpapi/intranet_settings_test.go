package httpapi

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/ids"
)

type unavailableSecretDeployments struct{ err error }

func (l unavailableSecretDeployments) List(context.Context, ids.ID) ([]deployment.Summary, error) {
	return nil, l.err
}

// Secret choices follow current placement records, not one connection's legacy
// release name. A failed owner read remains a failure rather than a shorter list.
func TestSecretChoicesIncludeEveryCustomerDeploymentAndPreserveReadFailure(t *testing.T) {
	h := Secrets{deployments: listedDeployments{{Name: "customer-east", Kind: deployment.Standing}, {Name: "customer-west", Kind: deployment.Standing}, {Name: "copy", Kind: deployment.Restored}}}
	got, err := h.secretDeployments(t.Context(), ids.Nil)
	if err != nil || !reflect.DeepEqual(got, []string{"preview", "main", "customer-east", "customer-west"}) {
		t.Fatalf("customer secret scopes: %v %v", got, err)
	}
	unavailable := errors.New("deployment inventory unavailable")
	h.deployments = unavailableSecretDeployments{unavailable}
	if got, err := h.secretDeployments(t.Context(), ids.Nil); !errors.Is(err, unavailable) || got != nil {
		t.Fatalf("unavailable owner yielded scopes: %v %v", got, err)
	}
}
