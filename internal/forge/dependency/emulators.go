package dependency

import (
	"context"
	"log"
)

// EmulatorKinds: every need the dependency catalog can run locally.
func (c *Catalog) EmulatorKinds(ctx context.Context) []string {
	var out []string
	kinds, err := c.All(ctx)
	if err != nil {
		log.Printf("read failed: %v", err)
	}
	for _, d := range kinds {
		if d.Local.Image != "" {
			out = append(out, d.Capability)
		}
	}
	return out
}
