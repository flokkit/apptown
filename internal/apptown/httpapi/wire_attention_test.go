package httpapi

import (
	"github.com/flokkit/apptown/internal/apptown/attention"
	"github.com/flokkit/apptown/internal/wiregen"
)

func init() {
	registerWireMapped(attention.Item{}, "AttentionItem")
	registerWireMapped(attention.Coverage{}, "AttentionCoverage")
	registerWire(
		wiregen.Named{Name: "AttentionView", Value: attention.View{}},
		wiregen.Named{Name: "AttentionItem", Value: attention.Item{}},
		wiregen.Named{Name: "AttentionCoverage", Value: attention.Coverage{}},
	)
}
