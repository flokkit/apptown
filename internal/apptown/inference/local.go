package inference

import (
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// Interface is one of this machine's network interfaces as LocalDefaults
// reads it: its name and the addresses on it, each with its prefix length
// (as net.Interface.Addrs gives them).
type Interface struct {
	Name     string
	Prefixes []netip.Prefix
}

// localSettings are the model-access settings a one-server install uses when
// its operator sets none.
type localSettings struct {
	Addr     string   // the listener: docker0's address, port 7779
	URL      string   // how containers reach it
	Networks []string // the CIDRs containers arrive from
}

// localPort is the default listener's port.
const localPort = 7779

// dockerBridge is how Docker names the bridges of user-defined networks
// (AppTown's chat networks among them): br- and the network ID's first 12
// hex digits.
var dockerBridge = regexp.MustCompile(`^br-[0-9a-f]{12}$`)

// dockerPools are Docker's default address pools: 172.17–31 as /16s and
// 192.168.0.0/16 as /20s (moby ipamutils). A daemon configured with other
// pools needs the settings given explicitly.
func dockerPools() []netip.Prefix {
	var pools []netip.Prefix
	for b := 17; b <= 31; b++ {
		pools = append(pools, netip.MustParsePrefix("172."+strconv.Itoa(b)+".0.0/16"))
	}
	for c := 0; c < 256; c += 16 {
		pools = append(pools, netip.MustParsePrefix("192.168."+strconv.Itoa(c)+".0/20"))
	}
	return pools
}

// LocalDefaults derives model access for an AppTown whose containers run on
// this machine's Docker, from this machine's interfaces alone. The listener
// is docker0's IPv4 address, which only this machine and its containers can
// reach, so a default never puts it on a public interface; containers reach
// it as host.docker.internal, which Docker maps to that same address. The
// allowed networks are the subnets of Docker's bridges here and Docker's
// default pools, less any pool that overlaps one of this machine's other
// interfaces (its LAN), whose addresses are never a container's. Interfaces
// are told apart by name: lo, docker0, br-<12 hex> and veth… are loopback or
// Docker's; any other is the host's. ok is false without a docker0 address
// (a Mac, no Docker, a container on its own network).
func LocalDefaults(interfaces []Interface) (localSettings, bool) {
	var gateway netip.Addr
	var bridges, host []netip.Prefix
	for _, in := range interfaces {
		for _, p := range in.Prefixes {
			if !p.Addr().Is4() {
				continue
			}
			switch {
			case in.Name == "docker0":
				if !gateway.IsValid() {
					gateway = p.Addr()
				}
				bridges = append(bridges, p.Masked())
			case dockerBridge.MatchString(in.Name):
				bridges = append(bridges, p.Masked())
			case in.Name == "lo" || strings.HasPrefix(in.Name, "veth") || p.Addr().IsLoopback():
			default:
				host = append(host, p.Masked())
			}
		}
	}
	if !gateway.IsValid() {
		return localSettings{}, false
	}
	networks := bridges
	for _, pool := range dockerPools() {
		if overlapsAny(pool, host) || containedIn(pool, networks) {
			continue
		}
		networks = append(networks, pool)
	}
	out := localSettings{
		Addr: netip.AddrPortFrom(gateway, localPort).String(),
		URL:  "http://host.docker.internal:" + strconv.Itoa(localPort),
	}
	seen := map[netip.Prefix]bool{}
	for _, n := range networks {
		if !seen[n] {
			seen[n] = true
			out.Networks = append(out.Networks, n.String())
		}
	}
	return out, true
}

func overlapsAny(p netip.Prefix, set []netip.Prefix) bool {
	for _, q := range set {
		if p.Overlaps(q) {
			return true
		}
	}
	return false
}

// containedIn: p adds nothing a prefix already listed covers.
func containedIn(p netip.Prefix, set []netip.Prefix) bool {
	for _, q := range set {
		if q.Bits() <= p.Bits() && q.Contains(p.Addr()) {
			return true
		}
	}
	return false
}
