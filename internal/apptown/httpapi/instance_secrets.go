package httpapi

import (
	"net/http"
)

// SetInstanceSecret: PUT /api/v1/secrets/{name} {value} — an administrator
// puts a credential AppTown itself uses (a target's API token, an SSH
// key) in its secrets file. Write-only: the answer names the secret, never
// its value, and a body the route cannot read is refused without echoing
// it. A name Infisical holds is refused, not shadowed.
func (h Secrets) SetInstanceSecret(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{errorText: `the body is {"value": "<the secret>"}`}, func(in struct {
		Value string `json:"value"`
	}) {
		name := r.PathValue("name")
		if err := h.secrets.SetInstance(r.Context(), name, in.Value, actor(r)); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, InstanceSecret{Name: name, Held: true})
	})
}

// RemoveInstanceSecret: DELETE /api/v1/secrets/{name} — an administrator
// takes a credential out of AppTown's secrets file.
func (h Secrets) RemoveInstanceSecret(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.secrets.RemoveInstance(r.Context(), name, actor(r)); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, InstanceSecret{Name: name, Held: false})
}

// InstanceSecret answers a write to AppTown's secrets file: which name,
// and whether the file now holds it. Never a value.
type InstanceSecret struct {
	Name string `json:"name"`
	Held bool   `json:"held"`
}
