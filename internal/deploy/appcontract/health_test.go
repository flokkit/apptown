package appcontract_test

import (
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/appcontract"
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

	// Legacy string still works (additive).
	ok(base + "    healthcheck: \"curl -f localhost:8080/ || exit 1\"\n")

	// Structured intent parses.
	ok(base + "    health:\n      readiness: { http: /healthz, expect: 200, checksDeps: true, startupGrace: 90s }\n      liveness: { tcp: 8080 }\n")

	// Guardrail: liveness.checksDeps is forbidden (restart-loop).
	bad("liveness.checksDeps is forbidden",
		base+"    health:\n      liveness: { tcp: 8080, checksDeps: true }\n")

	// Guardrail: exec readiness can't be frontDoor.
	bad("exec readiness probe cannot be requestContext:frontDoor",
		base+"    health:\n      readiness: { exec: [\"true\"], requestContext: frontDoor }\n")

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
