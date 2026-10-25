package deployment

import (
	"errors"
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"
)

func TestCustomerLabelsDoNotChangeDeploymentOrTargetIdentity(t *testing.T) {
	s, authority, in, target := configurationFixture(t)
	dep := configureForTest(t, s, in)
	before, err := s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.DisplayLabel != before.Name || target.DisplayLabel != target.Name {
		t.Fatal("new records lack customer labels")
	}
	renamed, err := s.EditDeploymentLabel(t.Context(), in.ProjectID, dep.ID, before.DisplayLabel, "  Customer production  ")
	if err != nil {
		t.Fatal(err)
	}
	expected := *before
	expected.DisplayLabel = "Customer production"
	if !reflect.DeepEqual(*renamed, expected) {
		t.Fatalf("label changed identity/runtime settings: before=%+v after=%+v", expected, renamed)
	}
	changed, err := s.EditTargetLabel(t.Context(), target.ID, target.DisplayLabel, "Private service")
	if err != nil {
		t.Fatal(err)
	}
	expectedTarget := *target
	expectedTarget.DisplayLabel = "Private service"
	if !reflect.DeepEqual(*changed, expectedTarget) {
		t.Fatalf("target label changed configuration: %+v %+v", expectedTarget, changed)
	}
	if _, err := s.EditDeploymentLabel(t.Context(), in.ProjectID, dep.ID, before.DisplayLabel, "stale"); err == nil {
		t.Fatal("stale label overwritten")
	}
	dbtest.FailNextStatement(t, s.db, "-- name: AppendReceipt")
	if _, err := s.EditDeploymentLabel(t.Context(), in.ProjectID, dep.ID, renamed.DisplayLabel, "unrecorded"); err == nil {
		t.Fatal("receipt failure hidden")
	}
	saved, err := s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil || saved.DisplayLabel != renamed.DisplayLabel {
		t.Fatalf("failed receipt changed label: %+v %v", saved, err)
	}
	authority.denial = errors.New("withdrawn")
	if _, err := s.EditTargetLabel(t.Context(), target.ID, changed.DisplayLabel, "unauthorized"); !errors.Is(err, authority.denial) {
		t.Fatalf("authority ignored: %v", err)
	}
}
