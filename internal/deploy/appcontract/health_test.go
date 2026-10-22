package appcontract_test

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// The structured per-part health schema parses, and its guardrails reject the
// dangerous declarations at parse time (design/subsystems/healthchecks.md §5.1).
func TestHealthSchemaValidation(t *testing.T) {
	ok := func(y string) {
		t.Helper()
		if _, err := appcontract.Parse([]byte(y)); err != nil {
			t.Fatalf("valid contract rejected: %v\n%s", err, y)
		}
	}
	bad := func(want, y string) {
		t.Helper()
		_, err := appcontract.Parse([]byte(y))
		if err == nil {
			t.Fatalf("expected error containing %q, got none\n%s", want, y)
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("want error containing %q, got %v", want, err)
		}
	}

	base := "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n"

	worker := "  worker:\n    build: .\n"

	// The legacy string is a non-public part's container health check; on
	// the public part no target reads it, so it is refused there.
	ok(base + worker + "    healthcheck: \"pgrep -f worker || exit 1\"\n")
	bad("healthcheck: is not used on the public part",
		base+"    healthcheck: \"curl -f localhost:8080/ || exit 1\"\n")

	// Structured intent parses: an http readiness, any liveness kind.
	ok(base + "    health:\n      readiness: { http: /healthz, expect: 200, startupGrace: 90s, requestContext: direct }\n      liveness: { tcp: 8080 }\n")
	ok(base + "    health:\n      readiness: { http: /healthz }\n      liveness: { exec: [\"true\"] }\n")

	// What no target uses yet is refused, not accepted and ignored.
	bad(`a health probe has no field "intervl"`,
		base+"    health:\n      readiness: { http: /healthz, intervl: 5s }\n")
	bad("health.readiness is an http probe",
		base+"    health:\n      readiness: { tcp: 8080 }\n")
	bad("health.readiness is an http probe",
		base+"    health:\n      readiness: { exec: [\"true\"] }\n")
	for _, field := range []string{"period: 10s", "timeout: 3s", "failureThreshold: 3", "successThreshold: 2", "checksDeps: true",
		// written at all is refused, a zero or false value included
		"period: \"\"", "failureThreshold: 0", "successThreshold: 0", "checksDeps: false"} {
		name := strings.SplitN(field, ":", 2)[0]
		bad("health.liveness."+name+" is not used by any target yet",
			base+"    health:\n      liveness: { http: /livez, "+field+" }\n")
	}

	// exactly one target.
	bad("exactly one of http, tcp or exec",
		base+"    health:\n      readiness: { expect: 200 }\n")
	bad("more than one of http/tcp/exec",
		base+"    health:\n      readiness: { http: /healthz, tcp: 8080 }\n")

	// bad requestContext + bad duration.
	bad("requestContext must be frontDoor or direct",
		base+"    health:\n      readiness: { http: /healthz, requestContext: sideDoor }\n")
	bad("startupGrace",
		base+"    health:\n      readiness: { http: /healthz, startupGrace: soon }\n")
}
