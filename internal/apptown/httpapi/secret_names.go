package httpapi

import (
	"net/http"
	"slices"
)

// SecretNames: GET /api/v1/secrets/names — the secret SELECTOR's
// source: key names only, values never leave the engine. Backing the
// connector forms' secretRef fields and the instance secrets list, which
// also says where each is held.
func (h Secrets) SecretNames(w http.ResponseWriter, r *http.Request) {
	held, err := h.secrets.InstanceNames(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	names := make([]string, 0, len(held))
	for k := range held {
		names = append(names, k)
	}
	slices.Sort(names)
	WriteJSON(w, 200, SecretNameList{Names: names, Stores: held})
}

// SecretNameList is the secret names a binding may reference, and the store
// each is read from (secrets.env, which wins, or infisical); never values.
type SecretNameList struct {
	Names  []string          `json:"names"`
	Stores map[string]string `json:"stores"`
}
