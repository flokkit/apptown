package inference

import (
	"net/netip"
	"slices"
	"testing"
)

func iface(name string, prefixes ...string) Interface {
	in := Interface{Name: name}
	for _, p := range prefixes {
		in.Prefixes = append(in.Prefixes, netip.MustParsePrefix(p))
	}
	return in
}

// A one-server install's model access needs no settings: the listener is on
// docker0's address only, containers reach it by host.docker.internal, and
// every network a container on this Docker can arrive from is allowed — the
// bridges here, surviving chat bridges included, and Docker's default pools
// — except pools that hold the host's own LAN.
func TestLocalDefaultsAllowThisDockersNetworksAndNotTheLAN(t *testing.T) {
	cases := []struct {
		name       string
		interfaces []Interface
		ok         bool
		addr       string
		has, lacks []string
	}{
		{name: "no docker0 (a Mac, or no Docker)", interfaces: []Interface{iface("lo", "127.0.0.1/8"), iface("en0", "192.168.1.20/24")}},
		{name: "docker0 with IPv6 only", interfaces: []Interface{iface("docker0", "fd00::1/64")}},
		{
			name:       "a LAN in 192.168.1.0/24 takes its /20 pool out",
			interfaces: []Interface{iface("lo", "127.0.0.1/8"), iface("eth0", "192.168.1.77/24", "fe80::1/64"), iface("docker0", "172.17.0.1/16"), iface("veth12ab", "169.254.3.1/16")},
			ok:         true, addr: "172.17.0.1:7779",
			has:   []string{"172.17.0.0/16", "172.18.0.0/16", "172.31.0.0/16", "192.168.16.0/20", "192.168.240.0/20"},
			lacks: []string{"192.168.0.0/20", "169.254.0.0/16"},
		},
		{
			name:       "docker0 on a custom bip has its subnet allowed",
			interfaces: []Interface{iface("docker0", "10.213.0.1/24"), iface("eth0", "203.0.113.5/24")},
			ok:         true, addr: "10.213.0.1:7779",
			has: []string{"10.213.0.0/24", "172.17.0.0/16", "192.168.0.0/20"},
		},
		{
			name: "a surviving chat bridge is Docker's, not the LAN",
			interfaces: []Interface{iface("docker0", "172.17.0.1/16"), iface("br-0123456789ab", "172.18.0.1/16"),
				iface("br-fedcba987654", "10.201.4.1/24"), iface("eth0", "192.168.1.5/24")},
			ok: true, addr: "172.17.0.1:7779",
			has:   []string{"172.17.0.0/16", "172.18.0.0/16", "10.201.4.0/24", "172.19.0.0/16"},
			lacks: []string{"192.168.0.0/20"},
		},
		{
			name:       "a host interface named like a bridge but not Docker's is the host's",
			interfaces: []Interface{iface("docker0", "172.17.0.1/16"), iface("br-lan", "172.20.0.1/16")},
			ok:         true, addr: "172.17.0.1:7779",
			lacks: []string{"172.20.0.0/16"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := LocalDefaults(c.interfaces)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v (%+v)", ok, c.ok, got)
			}
			if !ok {
				return
			}
			if got.Addr != c.addr || got.URL != "http://host.docker.internal:7779" {
				t.Fatalf("listener %s, URL %s", got.Addr, got.URL)
			}
			for _, n := range c.has {
				if !slices.Contains(got.Networks, n) {
					t.Errorf("networks lack %s: %v", n, got.Networks)
				}
			}
			for _, n := range c.lacks {
				if slices.Contains(got.Networks, n) {
					t.Errorf("networks include %s: %v", n, got.Networks)
				}
			}
			// What LocalDefaults gives, the gateway accepts.
			if _, err := ParseConfig(got.URL, got.Networks); err != nil {
				t.Fatalf("ParseConfig refuses the defaults: %v", err)
			}
		})
	}
}
