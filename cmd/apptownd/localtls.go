package main

import (
	"encoding/pem"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/localtls"
)

// localTLSDir is where -local-tls keeps its certificate authority, under
// the data directory. Its ca.pem is the certificate a person trusts.
const localTLSDir = "local-tls"

// localTLS loads the authority under -local-tls: the main listener's
// wrapper and what chats need to trust it and reach this AppTown's HTTPS
// addresses. Both nil when off. The public origin must be https: the
// session cookie, the origins apps are given and the forwarded scheme all
// follow it, and they must agree with the listener.
func localTLS(on bool, publicHost, addr, dataDir string, tierDomains []string) (func(net.Listener) net.Listener, *console.LocalTLS, error) {
	if !on {
		return nil, nil, nil
	}
	u, err := url.Parse(publicHost)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, nil, fmt.Errorf("-local-tls serves HTTPS, so -public-host must be an https:// origin (it is %q)", publicHost)
	}
	publicPort := 443
	if p := u.Port(); p != "" {
		if publicPort, err = strconv.Atoi(p); err != nil {
			return nil, nil, fmt.Errorf("-public-host %q: %w", publicHost, err)
		}
	}
	_, listenPort, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, nil, fmt.Errorf("-addr %q: %w", addr, err)
	}
	ca, err := localtls.Load(filepath.Join(dataDir, localTLSDir), nil)
	if err != nil {
		return nil, nil, err
	}
	config := ca.TLSConfig()
	chats := &console.LocalTLS{
		CACert:        pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Certificate().Raw}),
		Port:          publicPort,
		Target:        net.JoinHostPort("host.docker.internal", listenPort),
		PreviewDomain: previewDomain(tierDomains),
	}
	return func(l net.Listener) net.Listener { return localtls.Listener(l, config) }, chats, nil
}

// previewDomain is the preview tier's host name from -tier-domain values
// (preview=https://preview.localhost:7777), or "".
func previewDomain(tierDomains []string) string {
	for _, v := range tierDomains {
		for _, part := range strings.Split(v, ",") {
			name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
			if !ok || name != "preview" {
				continue
			}
			if u, err := url.Parse(value); err == nil && u.Hostname() != "" {
				return u.Hostname()
			}
			return strings.Split(value, ":")[0]
		}
	}
	return ""
}
