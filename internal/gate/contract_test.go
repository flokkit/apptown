package gate_test

import (
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/gate"
)

// The gate is built on its own (its image holds only this package), so it
// spells the names AppTown renders and sends rather than importing them.
// They are one contract: a renderer that sets GATE_SECRET, or a check that
// sends the probe header, under another name would leave the gate closed
// to everyone, AppTown included.
func TestGateReadsWhatAppTownSends(t *testing.T) {
	if gate.TokenHeader != deploy.GateProbeHeader {
		t.Fatalf("probe header: gate %q, forge %q", gate.TokenHeader, deploy.GateProbeHeader)
	}
	env := map[string]string{
		deploy.GateEnvUpstream: "http://web:8080",
		deploy.GateEnvSecret:   "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", // gitleaks:allow (test fixture, not a credential)
		deploy.GateEnvAppTown:  "https://forge.example",
		deploy.GateEnvListen:   ":8080",
	}
	cfg, err := gate.ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("the gate refuses the environment AppTown renders: %v", err)
	}
	if cfg.Listen != ":8080" {
		t.Fatalf("listen %q", cfg.Listen)
	}
}
