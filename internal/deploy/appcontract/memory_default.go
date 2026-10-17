package appcontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v3"
)

// MemoryDefault is a positive memory fallback supplied by adopted content.
// Unlimited memory is an operator decision, never a content default.
type MemoryDefault struct {
	Memory string `json:"memory,omitempty" yaml:"memory,omitempty"`
}

func (d MemoryDefault) Bytes() (int64, error) {
	if d.Memory == "" {
		return 0, nil
	}
	return ParseMemory(d.Memory)
}
func (d *MemoryDefault) UnmarshalJSON(raw []byte) error {
	type wire MemoryDefault
	var v wire
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return err
	}
	*d = MemoryDefault(v)
	_, err := d.Bytes()
	return err
}
func (d *MemoryDefault) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("resources default must be a memory declaration")
	}
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value != "memory" {
			return fmt.Errorf("unknown resource default %q", n.Content[i].Value)
		}
	}
	type wire MemoryDefault
	var v wire
	if err := n.Decode(&v); err != nil {
		return err
	}
	*d = MemoryDefault(v)
	_, err := d.Bytes()
	return err
}
