package main

import (
	"net/netip"
	"testing"

	"flokkit.io/forge/internal/forge/inference"
)

// The defaults apply only where nothing was said and this machine's Docker
// runs the work: one explicit setting, a worker, or no local Docker keeps
// today's rule that the operator gives all three.
func TestModelAccessDefaultsOnlyWhereNothingWasSaid(t *testing.T) {
	linux := []inference.Interface{{Name: "docker0", Prefixes: []netip.Prefix{netip.MustParsePrefix("172.17.0.1/16")}}}
	cases := []struct {
		name  string
		flags daemonFlags
		ifs   []inference.Interface
		want  bool
	}{
		{"nothing set on a Linux host with Docker", daemonFlags{}, linux, true},
		{"no docker0", daemonFlags{}, nil, false},
		{"networks given", daemonFlags{inferenceNetworks: "10.0.0.0/8"}, linux, false},
		{"address given", daemonFlags{inferenceAddr: "10.0.0.1:7779"}, linux, false},
		{"URL given", daemonFlags{inferenceURL: "http://x:7779"}, linux, false},
		{"a worker runs the work", daemonFlags{workerURL: "https://w:7790"}, linux, false},
		{"no local Docker", daemonFlags{noLocalDocker: true}, linux, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := c.flags
			line := defaultModelAccess(&o, c.ifs)
			if got := line != ""; got != c.want {
				t.Fatalf("defaulted = %v, want %v (%q)", got, c.want, line)
			}
			if c.want && (o.inferenceAddr != "172.17.0.1:7779" || o.inferenceURL != "http://host.docker.internal:7779" || o.inferenceNetworks == "") {
				t.Fatalf("settings %+v", o)
			}
			if !c.want && (o.inferenceAddr != c.flags.inferenceAddr || o.inferenceURL != c.flags.inferenceURL || o.inferenceNetworks != c.flags.inferenceNetworks) {
				t.Fatalf("settings changed: %+v", o)
			}
		})
	}
}
