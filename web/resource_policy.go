package web

import (
	_ "embed"
	"encoding/json"
)

// imagePolicy is shared with the Markdown renderer: the tour's public origin,
// never a wildcard for the storage provider behind it.
//
//go:embed resource-policy.json
var imagePolicy []byte

// ImageOrigins returns the remote image origins allowed by AppTown's UI.
func ImageOrigins() []string {
	var policy struct {
		ImageOrigins []string `json:"imageOrigins"`
	}
	if err := json.Unmarshal(imagePolicy, &policy); err != nil {
		panic(err) // malformed compiled policy is a build defect
	}
	return policy.ImageOrigins
}
