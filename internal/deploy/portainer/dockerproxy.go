package portainer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The Docker proxy (design/subsystems/deployment.md, "Backups and restore",
// Portainer): the forge copies a Portainer deployment's data with the same
// Docker commands it runs on a Docker host, through Portainer's own Docker
// API proxy for the environment (/api/endpoints/<id>/docker). The Docker
// CLI talks to a Unix socket in a private folder; DockerProxy forwards each
// request to Portainer with the target's API key, over Go's TLS (checked
// against the system's roots), so the key is in no file, argument or
// environment variable. Portainer's proxy carries plain requests and
// responses, not attached streams (docker exec, docker run -i): the
// commands that stream run as jobs instead (dataprotection).

// ErrCannotBackUp refuses data work in an environment the forge cannot
// reach for it.
var ErrCannotBackUp = errors.New("AppTown cannot back up deployments in this Portainer environment")

// Environment is one of Portainer's environments as the forge reads it for
// data work.
type Environment struct {
	ID   int64
	Name string
	// Type is Portainer's endpoint type: 1 a Docker socket, 2 an agent,
	// 4 an edge agent (5-7 are Kubernetes).
	Type  int
	Async bool // an edge agent in async mode: no Docker proxy at all
}

// ReadEnvironment reads the target's environment, by name.
func ReadEnvironment(ctx context.Context, cfg *Target) (Environment, error) {
	code, raw, err := JSON(ctx, cfg, "GET", endpointsPath, nil)
	if err != nil {
		return Environment{}, fmt.Errorf("listing Portainer environments: %w", err)
	}
	if code != 200 {
		return Environment{}, fmt.Errorf("listing Portainer environments: HTTP %d", code)
	}
	var eps []struct {
		ID   int64  `json:"Id"`
		Name string `json:"Name"`
		Type int    `json:"Type"`
		Edge struct {
			AsyncMode bool `json:"AsyncMode"`
		} `json:"Edge"`
	}
	if err := json.Unmarshal(raw, &eps); err != nil {
		return Environment{}, err
	}
	for _, e := range eps {
		if e.Name == cfg.EnvName {
			return Environment{ID: e.ID, Name: e.Name, Type: e.Type, Async: e.Edge.AsyncMode}, nil
		}
	}
	return Environment{}, fmt.Errorf("Portainer has no environment named %q", cfg.EnvName)
}

// DataReach says whether the forge can run Docker commands in the
// environment for data work: a Docker environment (socket, agent or
// standard edge agent) whose Docker answers through Portainer. nil when it
// can; ErrCannotBackUp with why otherwise.
func DataReach(ctx context.Context, cfg *Target) (Environment, error) {
	env, err := ReadEnvironment(ctx, cfg)
	if err != nil {
		return env, fmt.Errorf("%w %q: %v", ErrCannotBackUp, cfg.EnvName, err)
	}
	switch {
	case env.Type < 1 || env.Type > 4:
		return env, fmt.Errorf("%w %q: it is not a Docker environment (Portainer type %d)", ErrCannotBackUp, env.Name, env.Type)
	case env.Async:
		return env, fmt.Errorf("%w %q: it is an edge environment in async mode, which Portainer gives no Docker connection", ErrCannotBackUp, env.Name)
	}
	code, _, err := JSON(ctx, cfg, "GET", fmt.Sprintf("/api/endpoints/%d/docker/version", env.ID), nil)
	if err == nil && code != 200 {
		err = fmt.Errorf("HTTP %d", code)
	}
	if err != nil {
		return env, fmt.Errorf("%w %q: its Docker does not answer through Portainer: %v", ErrCannotBackUp, env.Name, err)
	}
	return env, nil
}

// DockerProxy serves one environment's Docker API on a Unix socket for the
// Docker CLI, forwarding each request through Portainer with the key the
// target's configuration resolves at that moment.
type DockerProxy struct {
	Socket string // the socket's path; docker --host unix://<Socket>
	server *http.Server
	ln     net.Listener
	once   sync.Once
}

// StartDockerProxy listens on a socket in dir (made private) and forwards
// to the environment's Docker API. config is read for every request, so a
// rotated key is used at once.
func StartDockerProxy(dir string, env Environment, config func(context.Context) (*Target, error)) (*DockerProxy, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	sock := filepath.Join(dir, fmt.Sprintf("portainer-%d.sock", env.ID))
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			cfg, err := config(r.In.Context())
			if err != nil {
				r.Out.URL = &url.URL{} // fails the request: no target
				return
			}
			base, err := url.Parse(cfg.PortainerURL)
			if err != nil {
				r.Out.URL = &url.URL{}
				return
			}
			r.Out.URL.Scheme, r.Out.URL.Host = base.Scheme, base.Host
			r.Out.URL.Path = strings.TrimRight(base.Path, "/") + fmt.Sprintf("/api/endpoints/%d/docker", env.ID) + r.In.URL.Path
			r.Out.URL.RawPath = ""
			r.Out.Host = base.Host
			r.Out.Header.Set("X-API-Key", cfg.PortainerKey)
		},
		FlushInterval: -1, // stream (a pull's progress, a container's logs) as it comes
	}
	p := &DockerProxy{Socket: sock, ln: ln, server: &http.Server{Handler: proxy}}
	go func() { _ = p.server.Serve(ln) }()
	return p, nil
}

// Host is the Docker CLI's --host for the proxy.
func (p *DockerProxy) Host() string { return "unix://" + p.Socket }

// Close stops serving and removes the socket.
func (p *DockerProxy) Close() error {
	var err error
	p.once.Do(func() {
		err = p.server.Close()
		_ = os.Remove(p.Socket)
	})
	return err
}
