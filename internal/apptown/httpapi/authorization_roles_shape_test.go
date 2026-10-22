package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The role picker exposes current roles and their capability meanings without
// offering migration-only roles for new grants.
func TestAuthorizationRoleListShape(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest("GET", "/api/v1/authz/roles", nil)
	w := httptest.NewRecorder()
	NewAuthorization(nil, nil).AuthzRoles(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"project-maintainer"`) || !strings.Contains(w.Body.String(), `"capabilities"`) {
		t.Fatalf("role catalog: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "legacy-amender") || strings.Contains(w.Body.String(), "migrated-project-member") {
		t.Fatal("retired roles exposed")
	}
}
