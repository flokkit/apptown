package deploy

import (
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
)

// Ingress is the admitted routing input for an artifact. It contains no
// credentials and is retained with the attempt instead of resolving mutable
// project or target settings when work is resumed.
type Ingress struct {
	Hosts             []string `json:"hosts"`
	URL               string   `json:"url"`
	CertificateDomain string   `json:"certificateDomain"`
}

// Clone separates admitted input from a caller's mutable host slice.
func (in Ingress) Clone() Ingress { in.Hosts = slices.Clone(in.Hosts); return in }

// Same compares exact ordered routing input; normalization happens at admission.
func (in Ingress) Same(other Ingress) bool {
	return in.URL == other.URL && in.CertificateDomain == other.CertificateDomain && slices.Equal(in.Hosts, other.Hosts)
}

// NormalizeHostname returns an exact DNS reservation key. Internal single-label
// names and ASCII/punycode are supported; URL syntax, IP literals and wildcard
// reservations are not. DNS provisioning remains the caller's responsibility.
func NormalizeHostname(raw string) (string, error) {
	host := strings.ToLower(strings.TrimSpace(raw))
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 || net.ParseIP(host) != nil {
		return "", errors.New("enter a DNS hostname without a scheme, port or path")
	}
	labels := strings.Split(host, ".")
	numeric := true
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("hostname labels must contain 1–63 letters, numbers or internal hyphens")
		}
		partNumeric := true
		for _, c := range label {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
				return "", errors.New("hostname must use ASCII DNS labels (punycode for international names)")
			}
			if c < '0' || c > '9' {
				partNumeric = false
			}
		}
		if strings.HasPrefix(label, "0x") {
			_, err := strconv.ParseUint(label[2:], 16, 64)
			partNumeric = err == nil
		}
		numeric = numeric && partNumeric
	}
	// All-numeric shorthand (such as 127.1) is interpreted as an IP address by
	// URL consumers even though it can pass a dotted-decimal parser.
	if numeric {
		return "", errors.New("use a DNS hostname rather than an IP address")
	}
	return host, nil
}
