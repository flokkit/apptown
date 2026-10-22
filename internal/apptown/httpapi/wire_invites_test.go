package httpapi

import "github.com/flokkit/apptown/internal/wiregen"

// The invite routes' responses.
func init() {
	registerWire(
		wiregen.Named{Name: "InviteLink", Value: InviteLink{}},
		wiregen.Named{Name: "InviteList", Value: InviteList{}},
		wiregen.Named{Name: "InviteOffer", Value: InviteOffer{}},
		wiregen.Named{Name: "SignedInAs", Value: SignedInAs{}},
	)
}
