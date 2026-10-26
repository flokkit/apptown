package digitalocean

import (
	"encoding/json"
	"errors"
	"fmt"
)

// componentHealth interprets the provider report only. It does not establish
// which deployment the report describes; the caller must bracket this read
// with exact deployment observations and the current deployment permit.
func componentHealth(body json.RawMessage, expected []string) (string, error) {
	var report struct {
		Health *struct {
			Components []struct {
				Name    string `json:"name"`
				State   string `json:"state"`
				Desired *int   `json:"replicas_desired"`
				Ready   *int   `json:"replicas_ready"`
			} `json:"components"`
		} `json:"app_health"`
	}
	if err := json.Unmarshal(body, &report); err != nil {
		return "unknown", fmt.Errorf("reading App Platform health report: %w", err)
	}
	if report.Health == nil || len(expected) == 0 {
		return "unknown", errors.New("App Platform health report has no runtime component evidence")
	}
	names := make(map[string]bool, len(expected))
	for _, name := range expected {
		if name == "" {
			return "unknown", errors.New("recorded App Platform runtime component has no name")
		}
		if _, exists := names[name]; exists {
			return "unknown", errors.New("recorded App Platform runtime component is duplicated")
		}
		names[name] = false
	}
	health := "healthy"
	for _, component := range report.Health.Components {
		seen, exists := names[component.Name]
		if !exists || seen {
			return "unknown", errors.New("App Platform health report has an unexpected or duplicate component")
		}
		names[component.Name] = true
		if component.Desired == nil || component.Ready == nil || *component.Desired <= 0 || *component.Ready < 0 || *component.Ready > *component.Desired {
			return "unknown", errors.New("App Platform health report has incomplete or invalid replica evidence")
		}
		switch component.State {
		case "HEALTHY":
		case "UNHEALTHY":
			health = "unhealthy"
		default:
			return "unknown", errors.New("App Platform component health is unknown")
		}
		if *component.Ready < *component.Desired {
			health = "unhealthy"
		}
	}
	for _, seen := range names {
		if !seen {
			return "unknown", errors.New("App Platform health report omits a required runtime component")
		}
	}
	return health, nil
}
