package integration

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/flokkit/apptown/internal/database"
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
func providerReadOK(err error) bool {
	found, err := database.Lookup(err)
	if err != nil {
		log.Printf("read failed: %v", err)
	}
	return found
}
