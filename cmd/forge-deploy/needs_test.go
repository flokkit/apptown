package main

import (
	"strings"
	"testing"
)

// forge-deploy runs no dependency catalogue: an app with no needs boots,
// and one with needs is refused, naming them.
func TestForgeDeployRefusesNeedsByName(t *testing.T) {
	if svcs, env, err := (noNeeds{}).Fulfil(t.Context(), nil, nil); err != nil || svcs != nil || env != nil {
		t.Fatalf("no needs: %v %v %v", svcs, env, err)
	}
	if _, _, err := (noNeeds{}).Fulfil(t.Context(), []string{"postgres", "s3"}, nil); err == nil || !strings.Contains(err.Error(), "this app needs postgres, s3") {
		t.Fatalf("needs: %v", err)
	}
}
