package appcontract

import "fmt"

// NeedSize selects a catalogue-owned container configuration. Source YAML sets
// only Name; Command is resolved by the dependency owner or restored from the
// generation's applied record, never supplied through the app contract YAML.
type NeedSize struct {
	Name    string   `json:"name"`
	Command []string `json:"command,omitempty"`
}

// ParseSizeName reads a size selector without coercing numbers or booleans.
func ParseSizeName(value any) (string, error) {
	name, ok := value.(string)
	if !ok || !profileNameRe.MatchString(name) {
		return "", fmt.Errorf("size must be a nonempty name (lower-case letters, digits and hyphens)")
	}
	return name, nil
}

// CheckNeedSizes refuses competing source declarations. Applied operator cap
// overrides are separate recorded decisions, not source declarations.
func (c *Contract) CheckNeedSizes() error {
	for _, name := range c.Needs {
		if _, selected := c.NeedSizes[name]; selected {
			if _, declared := c.NeedResources[name]; declared {
				return fmt.Errorf("needs: %s: choose size or resources, not both", name)
			}
		}
	}
	return nil
}
