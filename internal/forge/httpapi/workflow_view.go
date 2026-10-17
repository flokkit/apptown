package httpapi

import "flokkit.io/forge/internal/ids"

type WorkflowView struct {
	ID        ids.ID `json:"id"`
	ProjectID ids.ID `json:"projectId"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Version   int    `json:"version"`
	YAML      string `json:"yaml"`
	Origin    string `json:"origin"` // '' = instance-authored; else e.g. stock/change-request@3
	UpdatedAt string `json:"updatedAt"`
}
