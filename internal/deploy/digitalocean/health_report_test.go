package digitalocean

import (
	"encoding/json"
	"testing"
)

func TestAppPlatformHealthRequiresCompleteRuntimeEvidence(t *testing.T) {
	const web = `{"name":"web","state":"HEALTHY","replicas_desired":2,"replicas_ready":2}`
	const worker = `{"name":"worker","state":"HEALTHY","replicas_desired":1,"replicas_ready":1}`
	tests := []struct {
		name, body, want string
		invalid          bool
	}{
		{"service and worker", `{"app_health":{"components":[` + web + `,` + worker + `]}}`, "healthy", false},
		{"unhealthy worker", `{"app_health":{"components":[` + web + `,{"name":"worker","state":"UNHEALTHY","replicas_desired":1,"replicas_ready":1}]}}`, "unhealthy", false},
		{"partial replicas", `{"app_health":{"components":[` + web + `,{"name":"worker","state":"HEALTHY","replicas_desired":2,"replicas_ready":1}]}}`, "unhealthy", false},
		{"missing report", `{}`, "unknown", true},
		{"missing worker", `{"app_health":{"components":[` + web + `]}}`, "unknown", true},
		{"duplicate component", `{"app_health":{"components":[` + web + `,` + worker + `,` + worker + `]}}`, "unknown", true},
		{"unexpected component", `{"app_health":{"components":[` + web + `,` + worker + `,{"name":"other"}]}}`, "unknown", true},
		{"unknown state", `{"app_health":{"components":[` + web + `,{"name":"worker","state":"UNKNOWN","replicas_desired":1,"replicas_ready":1}]}}`, "unknown", true},
		{"missing counts", `{"app_health":{"components":[` + web + `,{"name":"worker","state":"HEALTHY"}]}}`, "unknown", true},
		{"zero desired", `{"app_health":{"components":[` + web + `,{"name":"worker","state":"HEALTHY","replicas_desired":0,"replicas_ready":0}]}}`, "unknown", true},
		{"negative ready", `{"app_health":{"components":[` + web + `,{"name":"worker","state":"UNHEALTHY","replicas_desired":1,"replicas_ready":-1}]}}`, "unknown", true},
		{"excess ready", `{"app_health":{"components":[` + web + `,{"name":"worker","state":"HEALTHY","replicas_desired":1,"replicas_ready":2}]}}`, "unknown", true},
		{"malformed", `{"app_health":`, "unknown", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := componentHealth(json.RawMessage(tt.body), []string{"web", "worker"})
			if got != tt.want || (err != nil) != tt.invalid {
				t.Fatalf("health=%q err=%v; want %q invalid=%v", got, err, tt.want, tt.invalid)
			}
		})
	}
	for _, names := range [][]string{nil, {"web", "web"}, {""}} {
		if _, err := componentHealth(json.RawMessage(`{"app_health":{"components":[`+web+`]}}`), names); err == nil {
			t.Fatalf("accepted invalid recorded component names %v", names)
		}
	}
}
