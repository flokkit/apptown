package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/inference"
)

// ModelDiscovery lists models through the gateway's own provider catalog,
// so the picker and the gateway agree on what a binding offers.
type ModelDiscovery struct{ catalog *inference.Catalog }

func NewModelDiscovery(catalog *inference.Catalog) ModelDiscovery {
	return ModelDiscovery{catalog: catalog}
}
func (h ModelDiscovery) ModelCatalog(w http.ResponseWriter, r *http.Request) {
	models, err := h.catalog.Models(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, ModelCatalogList{Models: models})
}
func (h ModelDiscovery) DiscoverModels(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{errorText: "bad json"}, func(in struct {
		Name   string         `json:"name"`
		Config map[string]any `json:"config"`
	}) {
		ids, err := h.catalog.Discover(r.Context(), in.Name, in.Config)
		if err != nil {
			// Unclassified failures keep the 502 this route has always answered.
			WriteFaultOr(w, err, http.StatusBadGateway)
			return
		}
		WriteJSON(w, 200, DiscoveredModels{Models: ids})
	})
}

// ModelCatalogList is the models the instance knows about.
type ModelCatalogList struct {
	Models []inference.ModelEntry `json:"models"`
}

// DiscoveredModels is the model ids a provider offered.
type DiscoveredModels struct {
	Models []string `json:"models"`
}
