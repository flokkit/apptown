package integration

import (
	"encoding/json"
	"fmt"
)

func nameSuffix(name string) string {
	if name == "" {
		return ""
	}
	return fmt.Sprintf(" named %q", name)
}

func encodeResource(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
