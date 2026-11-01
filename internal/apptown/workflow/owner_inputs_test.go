package workflow_test

import (
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func TestStartRequestKeepsOwnerFactsPrivate(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(workflow.StartRequest{})
	for _, name := range []string{"ReceiptActor", "RequestedBy", "Owner", "ParentRunID", "ParentStepID", "ParentStepAttempt", "ParentStepConfig", "AcceptedRunID"} {
		if field, ok := typ.FieldByName(name); ok && field.IsExported() {
			t.Errorf("outside caller can set %s.%s", typ.Name(), name)
		}
	}
}

func TestLandingRecheckKeepsOwnerFactsPrivate(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(workflow.LandingRecheck{})
	for _, name := range []string{"Who"} {
		if field, ok := typ.FieldByName(name); ok && field.IsExported() {
			t.Errorf("outside caller can set %s.%s", typ.Name(), name)
		}
	}
}
