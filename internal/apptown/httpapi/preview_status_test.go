package httpapi

import (
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"testing"
)

func TestPreviewDiscoveryDoesNotAssertRuntimeHealth(t *testing.T) {
	app := previewTierApp(&project.Project{Slug: "lunch"}, &workflow.Change{Number: 4, Title: "Menu"})
	if app.Observation != deployment.RuntimeUnknown || app.Running || app.ObservedAt != "" {
		t.Fatalf("unchecked preview claims health: %+v", app)
	}
	if app.Slug != "lunch" || app.ChangeNumber != 4 {
		t.Fatalf("preview identity lost: %+v", app)
	}
}
