package portainer

import (
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
)

func TestMissingApplyIdentityIsUncertain(t *testing.T) {
	t.Parallel()
	driver := &Driver{}
	_, err := driver.Resume(t.Context(), deploy.Placement{}, 1, nil, func(string, ...any) {})
	if err == nil || errors.Is(err, deploy.ErrNotApplied) || deploy.IsTerminal(err) {
		t.Fatalf("missing identity permitted retry or settlement: %v", err)
	}
}
