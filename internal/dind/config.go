package dind

import (
	"net"
	"os"
	"strings"
)

// hostNameservers: this machine's non-loopback resolvers from
// /etc/resolv.conf. A loopback stub (systemd-resolved's 127.0.0.53) is
// useless inside a container and is skipped; an empty answer means "let
// dockerd default", which is what happened before.
func hostNameservers() []string {
	return resolvConfField("nameserver", func(v string) bool {
		ip := net.ParseIP(v)
		return ip != nil && !ip.IsLoopback()
	})
}

func hostSearchDomains() []string {
	var out []string
	for _, line := range resolvConfField("search", func(string) bool { return true }) {
		out = append(out, strings.Fields(line)...)
	}
	return out
}

func resolvConfField(key string, keep func(string) bool) []string {
	b, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) < 2 || f[0] != key {
			continue
		}
		v := strings.Join(f[1:], " ")
		if keep(v) {
			out = append(out, v)
		}
	}
	return out
}

// DaemonOptions are a private daemon's DNS and mirror flags, the same for
// steps, tasks and chats: this machine's resolvers and search domains, and
// the mirror ("" none).
func DaemonOptions(mirror string) []string {
	var args []string
	for _, ns := range hostNameservers() {
		args = append(args, "--dns="+ns)
	}
	for _, d := range hostSearchDomains() {
		args = append(args, "--dns-search="+d)
	}
	// Route Docker Hub pulls through the org mirror when one is bound —
	// each daemon is cold, so upstream pulls would otherwise repeat per
	// step. Anonymous-read mirror; dockerd falls back to Hub silently if
	// the mirror declines.
	if m := mirror; m != "" {
		args = append(args, "--registry-mirror="+m)
		if h := strings.TrimPrefix(strings.TrimPrefix(m, "http://"), "https://"); strings.HasPrefix(m, "http://") {
			args = append(args, "--insecure-registry="+h)
		}
	}
	return args
}

// BuildkitConfig is the builder's configuration under the same policy.
func BuildkitConfig(mirror string) string {
	names := append([]string{"127.0.0.11"}, hostNameservers()...)
	toml := "[dns]\n  nameservers = [\"" + strings.Join(names, "\", \"") + "\"]\n"
	if sd := hostSearchDomains(); len(sd) > 0 {
		toml += "  searchDomains = [\"" + strings.Join(sd, "\", \"") + "\"]\n"
	}
	if m := mirror; m != "" {
		toml += "[registry.\"docker.io\"]\n  mirrors = [\"" +
			strings.TrimPrefix(strings.TrimPrefix(m, "https://"), "http://") + "\"]\n"
		if strings.HasPrefix(m, "http://") {
			toml += "[registry.\"" + strings.TrimPrefix(m, "http://") + "\"]\n  http = true\n"
		}
	}
	return toml
}
