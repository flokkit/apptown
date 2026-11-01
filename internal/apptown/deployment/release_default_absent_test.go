package deployment

import (
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/fault"
)

// Missing configuration names the explicit setup action, never a database
// detail or an inferred provider-specific environment.
func TestMissingReleaseDefaultIsSaidPlainly(t *testing.T) {
	t.Parallel()
	s, in, _, sources := requestFixture(t)
	s.configuration.Projects = sources
	_, err := s.RequestDefaultRelease(t.Context(), DefaultReleaseSelection{ProjectID: in.ProjectID})
	if !errors.Is(err, errNoReleaseDefault) || fault.KindOf(err) != fault.Unprocessable {
		t.Fatalf("no default: %v", err)
	}
	if msg := err.Error(); strings.Contains(msg, "sql") || !strings.Contains(msg, "choose a release environment") {
		t.Fatalf("the refusal reads %q", msg)
	}
}
