package main

import (
	"fmt"
	"net"
	"net/netip"
	"strings"

	"flokkit.io/forge/internal/forge/inference"
)

// defaultModelAccess fills in the three model-access settings on a
// one-server install whose operator set none of them: an AppTown that runs
// its containers on this machine's Docker (no -no-local-docker, no
// -worker-url) on a machine with a docker0 address (inference.LocalDefaults).
// Any one setting given keeps the rule that all three are explicit. It says
// what it chose, or nothing when it chose nothing.
func defaultModelAccess(o *daemonFlags, interfaces []inference.Interface) string {
	if o.inferenceAddr != "" || o.inferenceURL != "" || o.inferenceNetworks != "" || o.noLocalDocker || o.workerURL != "" {
		return ""
	}
	d, ok := inference.LocalDefaults(interfaces)
	if !ok {
		return ""
	}
	o.inferenceAddr, o.inferenceURL, o.inferenceNetworks = d.Addr, d.URL, strings.Join(d.Networks, ",")
	return fmt.Sprintf("model access by default: listening on %s (docker0) for this machine's containers, reached as %s; a Docker with its own address pools needs APPTOWN_INFERENCE_ADDR, _URL and _NETWORKS set (Setting up a server)", d.Addr, d.URL)
}

// machineInterfaces are this machine's interfaces and their addresses; one
// that cannot be read is left out.
func machineInterfaces() []inference.Interface {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []inference.Interface
	for _, in := range ifaces {
		addrs, err := in.Addrs()
		if err != nil {
			continue
		}
		got := inference.Interface{Name: in.Name}
		for _, a := range addrs {
			if p, err := netip.ParsePrefix(a.String()); err == nil {
				got.Prefixes = append(got.Prefixes, p)
			}
		}
		out = append(out, got)
	}
	return out
}
