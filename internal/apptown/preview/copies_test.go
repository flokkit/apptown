package preview

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
)

// A copy's previews are served on the eval domain only: with none, no
// preview of a copy boots, refused as Deployment refuses a copy's placement.
func TestACopysPreviewNeedsTheEvalDomain(t *testing.T) {
	t.Parallel()
	w := newWorld(t, "", "")
	copy := createProjectFor(t, w.db, "p-copy", "P copy", project.EvalCopy)
	s := w.service(Dependencies{})
	if err := s.refusesCopyPreview(context.Background(), copy); !errors.Is(err, deployment.ErrReleaseRefused) || !strings.Contains(err.Error(), "eval domain") {
		t.Fatalf("no eval domain: %v", err)
	}
	if err := s.refusesCopyPreview(context.Background(), w.projectID); err != nil {
		t.Fatalf("a real project's preview: %v", err)
	}
	w.reconfigure(s, nil, func(e *deployment.EnvironmentDependencies) {
		e.Routes.EvalDomain, e.Routes.EvalScheme = "eval.example.com", "https"
	})
	if err := s.refusesCopyPreview(context.Background(), copy); err != nil {
		t.Fatalf("with an eval domain: %v", err)
	}
}
