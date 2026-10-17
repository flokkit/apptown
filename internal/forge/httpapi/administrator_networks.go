package httpapi

import (
	"net"
	"net/http"

	"flokkit.io/forge/internal/forge/identity"
)

type AdministratorNetworks struct {
	access  *identity.Service
	address ClientAddress
}

func NewAdministratorNetworks(access *identity.Service, address ClientAddress) AdministratorNetworks {
	return AdministratorNetworks{access: access, address: address}
}

// AdminNetworks: GET/PUT /api/v1/auth/admin-networks
// {networks: ["192.168.1.0/24"]}. A PUT that would lock the caller out is
// refused: the person changing the fence must be standing inside it.
func (h AdministratorNetworks) AdminNetworks(w http.ResponseWriter, r *http.Request) {
	ip := h.address.ClientIP(r)
	if r.Method == http.MethodGet {
		networks, err := h.access.AdministratorNetworks(r.Context())
		if err != nil {
			WriteError(w, 500, "reading administrator networks: %v", err)
			return
		}
		WriteJSON(w, 200, AdministratorNetworksView{Networks: networks, You: ip.String(), YouInside: networks.Allows(ip)})
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Expected *identity.AdministratorNetworks `json:"expected"`
		Networks []string                        `json:"networks"`
	}) {
		clean, err := h.access.SetAdministratorNetworks(r.Context(), Principal(r), in.Expected, in.Networks, ip)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, AdministratorNetworksView{Networks: clean, You: ip.String(), YouInside: true})
	})
}

type ClientAddress interface{ ClientIP(*http.Request) net.IP }

// AdministratorNetworksView is the fence around administration and where the
// caller stands relative to it.
type AdministratorNetworksView struct {
	Networks  identity.AdministratorNetworks `json:"networks"`
	You       string                         `json:"you"`
	YouInside bool                           `json:"youInside"`
}
