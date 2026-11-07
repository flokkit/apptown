package httpapi

import (
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/wiregen"
)

func init() { registerWire(wiregen.Named{Name: "Feature", Value: identity.Feature{}}) }
