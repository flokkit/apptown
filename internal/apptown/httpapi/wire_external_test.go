package httpapi

import (
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/wiregen"
)

func init() {
	registerWire(
		wiregen.Named{Name: "ExternalProvider", Value: identity.ExternalProvider{}},
		wiregen.Named{Name: "ExternalProviderChange", Value: identity.ExternalProviderChange{}},
		wiregen.Named{Name: "ExternalProviderSummary", Value: ExternalProviderSummary{}},
		wiregen.Named{Name: "ExternalStartRequest", Value: ExternalStartRequest{}},
		wiregen.Named{Name: "ExternalRedirect", Value: ExternalRedirect{}},
		wiregen.Named{Name: "ExternalEnableRequest", Value: ExternalEnableRequest{}},
		wiregen.Named{Name: "ExternalBindRequest", Value: ExternalBindRequest{}},
		wiregen.Named{Name: "ExternalBindingList", Value: ExternalBindingList{}},
		wiregen.Named{Name: "ExternalBindingView", Value: identity.ExternalBindingView{}},
	)
}
