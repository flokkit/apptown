package dependency

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"flokkit.io/forge/internal/deploy/appcontract"
	"gopkg.in/yaml.v3"
)

// Size is a catalogue-owned local-container preset. Command is complete literal
// argv; it cannot change the need's image, connection, credentials or data.
type Size struct {
	Description string                    `yaml:"description" json:"description"`
	Resources   appcontract.MemoryDefault `yaml:"resources" json:"resources"`
	Command     []string                  `yaml:"command" json:"command"`
}

func (s *Size) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("size must be a mapping")
	}
	for i := 0; i < len(n.Content); i += 2 {
		switch n.Content[i].Value {
		case "description", "resources", "command":
		default:
			return fmt.Errorf("unknown size field %q", n.Content[i].Value)
		}
	}
	type wire Size
	var v wire
	if err := n.Decode(&v); err != nil {
		return err
	}
	*s = Size(v)
	return nil
}

func (k *Kind) checkSizes() error {
	for name, size := range k.Sizes {
		if _, err := appcontract.ParseSizeName(name); err != nil {
			return fmt.Errorf("size %q: %w", name, err)
		}
		memory, err := size.Resources.Bytes()
		if err != nil || memory <= 0 {
			return fmt.Errorf("size %s requires a finite positive memory limit", name)
		}
		if k.Local.Image == "" || len(size.Command) == 0 {
			return fmt.Errorf("size %s requires a local image and a complete command", name)
		}
		for _, arg := range size.Command {
			if arg == "" {
				return fmt.Errorf("size %s has an empty command argument", name)
			}
		}
	}
	return nil
}

// ResolveSizes resolves source selectors after profile selection. Resolved values
// (including an applied generation's snapshot) are never reinterpreted using the
// current catalogue. The returned contract owns its changed maps and argv.
func (c *Catalog) ResolveSizes(ctx context.Context, in *appcontract.Contract) (*appcontract.Contract, error) {
	out := *in
	out.NeedSizes = maps.Clone(in.NeedSizes)
	out.NeedResources = maps.Clone(in.NeedResources)
	for need, selected := range in.NeedSizes {
		if !slices.Contains(in.Needs, need) {
			return nil, fmt.Errorf("size declared for absent need %s", need)
		}
		selected.Command = slices.Clone(selected.Command)
		if len(selected.Command) == 0 {
			if _, exists := in.NeedResources[need]; exists {
				return nil, fmt.Errorf("needs: %s: choose size or resources, not both", need)
			}
			kind, err := c.ByCapability(ctx, need)
			if err != nil {
				return nil, fmt.Errorf("resolving size for %s: %w", need, err)
			}
			size, ok := kind.Sizes[selected.Name]
			if !ok || kind.Local.Image == "" {
				return nil, fmt.Errorf("need %s has no local-container size %q", need, selected.Name)
			}
			memory, err := size.Resources.Bytes()
			if err != nil || memory <= 0 || len(size.Command) == 0 {
				return nil, fmt.Errorf("need %s size %s is invalid", need, selected.Name)
			}
			selected.Command = slices.Clone(size.Command)
			if out.NeedResources == nil {
				out.NeedResources = map[string]appcontract.Resources{}
			}
			out.NeedResources[need] = appcontract.Resources{MemoryBytes: memory}
		}
		out.NeedSizes[need] = selected
	}
	return &out, nil
}
