package dependency

import "context"

// EmulatorKinds: every need the dependency catalog can run locally. A
// catalog that cannot be read is an error, never a shorter list.
func (c *Catalog) EmulatorKinds(ctx context.Context) ([]string, error) {
	kinds, err := c.All(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range kinds {
		if d.Local.Image != "" {
			out = append(out, d.Capability)
		}
	}
	return out, nil
}
