package docker

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
)

// Each command gets a private local endpoint. Disabling upstream keep-alive
// releases a session after its HTTP response, rather than letting idle
// connections starve a stats call with more containers than session slots.
func commandProxy(ctx context.Context, dial func(context.Context) (net.Conn, error)) (string, func(), error) {
	dir, err := os.MkdirTemp("", "apptown-docker-")
	if err != nil {
		return "", nil, err
	}
	socket := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) }}
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: "docker"})
	proxy.Transport = transport
	server := &http.Server{Handler: proxy, BaseContext: func(net.Listener) context.Context { return ctx }}
	go server.Serve(listener)
	return "unix://" + socket, func() { server.Close(); transport.CloseIdleConnections(); os.RemoveAll(dir) }, nil
}
