package apptown

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
)

// Where administrators may sign in: a list of networks, kept in
// instance_settings, that an administrator account must be connecting from
// to sign in with a password, consent to an MCP client, or present an
// administrator token. Empty means anywhere. Loopback always
// passes, so a person at the host itself can never be locked out.
//
// The caller's address is taken from the connection unless the connection
// comes from a trusted proxy (loopback, this host's own addresses, or
// -trusted-proxies), in which case the proxy's X-Real-IP (else the
// rightmost X-Forwarded-For entry) is the caller. A Traefik running with
// host networking on the same host is seen by apptownd as the host itself.

func parseCIDRs(list []string) []*net.IPNet {
	var out []*net.IPNet
	for _, c := range list {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !strings.Contains(c, "/") {
			if ip := net.ParseIP(c); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				c = fmt.Sprintf("%s/%d", ip, bits)
			}
		}
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func inNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

type proxyPolicy struct {
	networks []*net.IPNet
	hosts    []net.IP
}

// Host discovery is a construction effect, never package initialization or
// request work. Failure trusts no additional hosts; loopback still works.
func hostAddressSnapshot(hosts []net.IP) []net.IP {
	if hosts == nil {
		addrs, err := net.InterfaceAddrs()
		if err != nil {
			return nil
		}
		for _, addr := range addrs {
			if n, ok := addr.(*net.IPNet); ok {
				hosts = append(hosts, n.IP)
			}
		}
	}
	snapshot := make([]net.IP, len(hosts))
	for i, ip := range hosts {
		snapshot[i] = slices.Clone(ip)
	}
	return snapshot
}

func (p proxyPolicy) isTrustedProxy(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || inNets(ip, p.networks) {
		return true
	}
	for _, h := range p.hosts {
		if h.Equal(ip) {
			return true
		}
	}
	return false
}

// clientIP: who is really on the other end of this request.
func (p proxyPolicy) ClientIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote := net.ParseIP(strings.TrimSpace(host))
	if !p.isTrustedProxy(remote) {
		return remote
	}
	if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
		if ip := net.ParseIP(v); ip != nil {
			return ip
		}
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ip := net.ParseIP(strings.TrimSpace(parts[i]))
			if ip == nil {
				continue
			}
			if p.isTrustedProxy(ip) {
				continue // a hop, not the caller
			}
			return ip
		}
	}
	return remote
}

func (a *App) adminAllowedFrom(ctx context.Context, ip net.IP) bool {
	policy, err := a.Access.AdministratorNetworks(ctx)
	if err != nil {
		readOK(err)
		return false
	}
	return policy.Allows(ip)
}
func (a *App) adminDenialText(ctx context.Context, ip net.IP) string {
	policy, err := a.Access.AdministratorNetworks(ctx)
	if err != nil {
		return "the administrator network policy could not be read"
	}
	return policy.Denial(ip)
}
