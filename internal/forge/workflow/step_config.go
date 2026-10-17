package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// DecodeStepConfig reads a step's stored configuration into v. An empty
// configuration is {}. One that does not decode is an error a runner settles
// its step with: its zero value is not the configuration (a tests step with
// no command reads as "no tests declared" and would pass).
func DecodeStepConfig(raw json.RawMessage, v any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("its configuration could not be read: %w", err)
	}
	return nil
}
