package forge

import (
	"strings"
	"testing"
)

func TestLoopbackAddr(t *testing.T) {
	t.Parallel()
	for addr, want := range map[string]bool{
		"127.0.0.1:7777": true,
		"localhost:7777": true,
		":7777":          false,
		"0.0.0.0:7777":   false,
		"192.168.1.5:80": false,
	} {
		if got := loopbackAddr(addr); got != want {
			t.Errorf("loopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

// Open authentication serves only this machine unless the operator says
// otherwise: in open mode a caller names itself, so a listener another
// machine can reach is full control for anyone who reaches it. The other
// modes, and loopback listeners, start as they always have.
//
// Kills: a warning on stderr as the only guard, and a check that looked at
// the main listener but not the console's.
func TestOpenModeServesOnlyThisMachineUnlessAsked(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mode    string
		addr    string
		console string
		allowed bool
		refused []string // listeners the refusal must name
	}{
		{"the old default", "open", ":7777", ":7778", false, []string{`-addr ":7777"`, `-console-addr ":7778"`}},
		{"a loopback main beside a reachable console", "open", "127.0.0.1:7777", ":7778", false, []string{`-console-addr ":7778"`}},
		{"an address on the network", "open", "192.168.1.5:7777", "", false, []string{`-addr "192.168.1.5:7777"`}},
		{"an empty auth mode is open mode", "", ":7777", "", false, []string{`-addr ":7777"`}},
		{"an empty main address is every interface", "open", "", "", false, []string{`-addr ""`}},
		{"loopback, both", "open", "127.0.0.1:7777", "127.0.0.1:7778", false, nil},
		{"IPv6 loopback and localhost", "open", "[::1]:7777", "localhost:7778", false, nil},
		{"no console listener", "open", "127.0.0.1:7777", "", false, nil},
		{"the operator accepts it", "open", ":7777", ":7778", true, nil},
		{"internal mode on every interface", "internal", ":7777", ":7778", false, nil},
		{"trusted-header mode", "trusted-header", ":7777", ":7778", false, nil},
	}
	for _, c := range cases {
		err := OpenExposure(c.mode, c.addr, c.console, c.allowed)
		if len(c.refused) == 0 {
			if err != nil {
				t.Errorf("%s: refused: %v", c.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: started", c.name)
			continue
		}
		for _, want := range append(c.refused, "-open-on-network", "127.0.0.1") {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal does not name %q: %v", c.name, want, err)
			}
		}
	}
}
