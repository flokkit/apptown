package apptown

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/apptown/integration"
)

func TestModelCatalogCacheStaysWithApplication(t *testing.T) {
	t.Parallel()
	first, second := testApp(t), testApp(t)
	for i, a := range []*App{first, second} {
		name := []string{"first", "second"}[i]
		if _, err := a.Integrations.Bindings.Save(context.Background(), name, integration.CapModel, name, mustJSON(map[string]string{"model": name + "/model"}), "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range []*App{first, second, first} {
		response := httptest.NewRecorder()
		httpapi.NewModelDiscovery(a.providers).ModelCatalog(response, httptest.NewRequest("GET", "/api/v1/model-catalog", nil))
		var body struct{ Models []map[string]string }
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != 200 || len(body.Models) != 1 {
			t.Fatalf("catalog: %d %s %v", response.Code, response.Body.String(), err)
		}
		want := "first/model"
		if a == second {
			want = "second/model"
		}
		if got := body.Models[0]["pattern"]; got != want {
			t.Fatalf("catalog from another application: got %q, want %q", got, want)
		}
	}
}
