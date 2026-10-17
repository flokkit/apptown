package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

// A stored configuration that does not decode is an error, never the zero
// configuration; an empty one is {}.
func TestAStepConfigurationThatDoesNotDecodeIsAnError(t *testing.T) {
	t.Parallel()
	var cfg struct {
		Command string `json:"command"`
	}
	for _, raw := range []string{"", "  ", "{}"} {
		if err := DecodeStepConfig(json.RawMessage(raw), &cfg); err != nil {
			t.Errorf("%q: %v", raw, err)
		}
	}
	if err := DecodeStepConfig(json.RawMessage(`{"command":"go test"}`), &cfg); err != nil || cfg.Command != "go test" {
		t.Fatalf("decoded %+v %v", cfg, err)
	}
	for _, raw := range []string{"not json", `{"command": 7}`, `[1]`} {
		if err := DecodeStepConfig(json.RawMessage(raw), &cfg); err == nil || !strings.Contains(err.Error(), "configuration could not be read") {
			t.Errorf("%q decoded: %v", raw, err)
		}
	}
}
