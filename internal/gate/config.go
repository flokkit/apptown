// Package gate is AppTown access gate: a small reverse proxy that is the
// only public component of a restored copy of production data. The app runs
// behind it as an internal service. The gate lets in two kinds of caller and
// nobody else:
//
//   - AppTown's own probes, which carry the copy's gate secret in the
//     AppTown-Restore-Token header;
//   - people AppTown has sent through /_apptown/enter with a single-use
//     ticket, which the gate redeems with AppTown for a session cookie.
//     AppTown's own check of the gate enters the same way, with a ticket
//     it issued itself; the gate does not tell the two apart.
//
// Everything else gets an empty 404, so an anonymous caller learns nothing
// about the app behind it. Every refusal carries the AppTown-Gate: refused
// header, which the gate strips from the app's own answers, so AppTown's
// check of the gate can tell a refusal from an app answering 404. The gate logs no request content: the copy holds
// production data, and paths, headers and bodies can carry it.
package gate

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Config is what the gate needs to run. ConfigFromEnv builds it from the
// environment the platform gives the gate component.
type Config struct {
	// Listen is the address the gate serves on.
	Listen string
	// Upstream is the app behind the gate, reached over the platform's
	// internal network.
	Upstream *url.URL
	// Secret is the copy's gate secret: probes present it, and the gate
	// presents it to AppTown when redeeming a ticket. Never logged.
	Secret string
	// AppTown is AppTown's public origin, where tickets are redeemed.
	AppTown *url.URL
}

// DefaultListen is the address used when GATE_LISTEN is not set.
const DefaultListen = ":8080"

// ConfigFromEnv reads GATE_LISTEN, GATE_UPSTREAM, GATE_SECRET and
// GATE_APPTOWN through getenv. It refuses a missing or malformed value rather
// than starting a gate that would forward to the wrong place or accept a
// weak secret. Error messages never include the secret's value.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	cfg := Config{Listen: strings.TrimSpace(getenv("GATE_LISTEN"))}
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	var problems []error

	upstream, err := parseUpstream(getenv("GATE_UPSTREAM"))
	if err != nil {
		problems = append(problems, fmt.Errorf("GATE_UPSTREAM: %w", err))
	}
	cfg.Upstream = upstream

	secret := getenv("GATE_SECRET")
	if err := checkSecret(secret); err != nil {
		problems = append(problems, fmt.Errorf("GATE_SECRET: %w", err))
	}
	cfg.Secret = secret

	origin, err := parseAppTown(getenv("GATE_APPTOWN"))
	if err != nil {
		problems = append(problems, fmt.Errorf("GATE_APPTOWN: %w", err))
	}
	cfg.AppTown = origin

	if len(problems) > 0 {
		return Config{}, errors.Join(problems...)
	}
	return cfg, nil
}

func parseUpstream(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, errors.New("required, the app's internal URL such as http://web:8080")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("not a URL")
	}
	// The app is reached over the platform's private network, which does not
	// terminate TLS for internal services.
	if u.Scheme != "http" || u.Host == "" {
		return nil, errors.New("must be an http URL with a host, such as http://web:8080")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("must not carry credentials, a query or a fragment")
	}
	return u, nil
}

// checkSecret insists on the form AppTown issues: 32 random bytes as 64
// lowercase hex characters. Anything else is a delivery mistake, and a
// short or empty secret would make the probe check meaningless.
func checkSecret(s string) error {
	if s == "" {
		return errors.New("required, 64 lowercase hex characters")
	}
	if len(s) != 64 {
		return errors.New("must be exactly 64 lowercase hex characters")
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return errors.New("must be exactly 64 lowercase hex characters")
		}
	}
	return nil
}

func parseAppTown(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, errors.New("required, AppTown's public origin such as https://forge.example")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("must be an origin such as https://forge.example")
	}
	// The gate sends the secret to this address on every redemption, so it
	// must travel over TLS. Plain http is allowed only for a loopback AppTown,
	// which is what tests use.
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return nil, errors.New("must use https (http is allowed only for localhost)")
		}
	default:
		return nil, errors.New("must use https")
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("must be an origin only, with no path, query or credentials")
	}
	u.Path = ""
	return u, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
