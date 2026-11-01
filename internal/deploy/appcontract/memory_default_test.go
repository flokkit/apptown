package appcontract

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMemoryDefaultsArePositiveMemoryOnly(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{"memory":"none"}`, `{"memory":"-1M"}`, `{"memory":"1M"}`, `{"cpu":1}`, `{"env":{"A":"B"}}`} {
		var d MemoryDefault
		if err := json.Unmarshal([]byte(raw), &d); err == nil {
			t.Fatalf("invalid memory default accepted: %s", raw)
		}
	}
	for _, raw := range []string{"memory: none", "cpu: 1", "memory: -1M"} {
		var d MemoryDefault
		if err := yaml.Unmarshal([]byte(raw), &d); err == nil {
			t.Fatalf("invalid YAML default accepted: %s", raw)
		}
	}
	var d MemoryDefault
	if err := json.Unmarshal([]byte(`{"memory":"160M"}`), &d); err != nil {
		t.Fatal(err)
	}
	if b, err := d.Bytes(); err != nil || b != 160<<20 {
		t.Fatalf("default parsed as %d %v", b, err)
	}
}
