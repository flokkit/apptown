package portainer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// The Docker proxy (design/subsystems/deployment.md, "Backups and restore",
// Portainer): AppTown copies a Portainer deployment's data with the same
// Docker commands it runs on a Docker host, through Portainer's own Docker
// API proxy for the environment (/api/endpoints/<id>/docker). The Docker
// CLI talks to a Unix socket in a private folder; DockerProxy forwards each
// request to Portainer with the target's API key, over Go's TLS (checked
// against the system's roots), so the key is in no file, argument or
// environment variable. Portainer's proxy carries plain requests and
// responses, not attached streams (docker exec, docker run -i): the
// commands that stream run as jobs instead (dataprotection).

// ErrCannotBackUp refuses data work in an environment AppTown cannot
// reach for it.
var ErrCannotBackUp = errors.New("AppTown cannot back up deployments in this Portainer environment")

// deploymentEnvironment is one of Portainer's environments as AppTown reads it for
// data work.
type deploymentEnvironment struct {
	ID   int64
	Name string
	// Type is Portainer's endpoint type: 1 a Docker socket, 2 an agent,
	// 4 an edge agent (5-7 are Kubernetes).
	Type  int
	Async bool // an edge agent in async mode: no Docker proxy at all
}

// ReadEnvironment reads the target's environment, by name.
func ReadEnvironment(ctx context.Context, cfg *Target) (deploymentEnvironment, error) {
	if cfg.Scope != nil {
		code, raw, err := JSON(ctx, cfg, "GET", fmt.Sprintf("/api/endpoints/%d", cfg.Scope.EndpointID), nil)
		if err != nil {
			return deploymentEnvironment{}, err
		}
		if code != 200 {
			return deploymentEnvironment{}, fmt.Errorf("reading pinned Portainer endpoint: HTTP %d", code)
		}
		var e struct {
			ID   int64 `json:"Id"`
			Name string
			Type int
			Edge struct{ AsyncMode bool }
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return deploymentEnvironment{}, err
		}
		if e.ID != cfg.Scope.EndpointID {
			return deploymentEnvironment{}, errors.New("Portainer returned a different endpoint")
		}
		return deploymentEnvironment{ID: e.ID, Name: e.Name, Type: e.Type, Async: e.Edge.AsyncMode}, nil
	}

	code, raw, err := JSON(ctx, cfg, "GET", endpointsPath, nil)
	if err != nil {
		return deploymentEnvironment{}, fmt.Errorf("listing Portainer environments: %w", err)
	}
	if code != 200 {
		return deploymentEnvironment{}, fmt.Errorf("listing Portainer environments: HTTP %d", code)
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
		return deploymentEnvironment{}, err
	}
	for _, e := range eps {
		if e.Name == cfg.EnvName {
			return deploymentEnvironment{ID: e.ID, Name: e.Name, Type: e.Type, Async: e.Edge.AsyncMode}, nil
		}
	}
	return deploymentEnvironment{}, fmt.Errorf("Portainer has no environment named %q", cfg.EnvName)
}

// DataReach says whether AppTown can run Docker commands in the
// environment for data work: a Docker environment (socket, agent or
// standard edge agent) whose Docker answers through Portainer. nil when it
// can; ErrCannotBackUp with why otherwise.
func DataReach(ctx context.Context, cfg *Target) (deploymentEnvironment, error) {
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
func StartDockerProxy(dir string, env deploymentEnvironment, config func(context.Context) (*Target, error)) (*DockerProxy, error) {
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
			if err != nil || (cfg != nil && cfg.Scope != nil && cfg.Scope.EndpointID != env.ID) {
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
			if r.In.Method == http.MethodPost && r.In.ContentLength == 0 && containerStart.MatchString(r.In.URL.Path) {
				// Portainer (2.45 and later) passes an empty body on to an
				// agent with no length, and Docker refuses a start whose
				// body is of unknown length or longer than 7 bytes. An
				// empty object of known length is a start with no options.
				r.Out.Body = io.NopCloser(strings.NewReader("{}"))
				r.Out.ContentLength = 2
				r.Out.Header.Set("Content-Type", "application/json")
			}
		},
		ModifyResponse: withPortainersReason,
		FlushInterval:  -1, // stream (a pull's progress, a container's logs) as it comes
	}
	p := &DockerProxy{Socket: sock, ln: ln, server: &http.Server{Handler: proxy}}
	go func() { _ = p.server.Serve(ln) }()
	return p, nil
}

// containerStart is Docker's start route, with or without the API version
// the CLI puts first.
var containerStart = regexp.MustCompile(`^(/v[0-9.]+)?/containers/[^/]+/start$`)

// withPortainersReason puts the reason Portainer gives for refusing a
// request into the message the Docker CLI prints. Portainer answers
// {"message":"Proxy failure","details":"Forbidden to use bind mounts"},
// and the CLI prints only the message. Other answers pass unchanged.
func withPortainersReason(resp *http.Response) error {
	if resp.StatusCode < 400 || resp.Header.Get("Content-Encoding") != "" || !strings.Contains(resp.Header.Get("Content-Type"), "json") {
		return nil
	}
	const most = 1 << 16 // an error answer is small; a larger one passes as it is
	raw, err := io.ReadAll(io.LimitReader(resp.Body, most))
	if err != nil {
		return err
	}
	if len(raw) == most {
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(raw), resp.Body), resp.Body}
		return nil
	}
	resp.Body.Close()
	var answer map[string]any
	if json.Unmarshal(raw, &answer) == nil {
		message, _ := answer["message"].(string)
		details, _ := answer["details"].(string)
		if message != "" && details != "" && details != message {
			answer["message"] = message + ": " + details
			if rewritten, err := json.Marshal(answer); err == nil {
				raw = rewritten
			}
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	resp.ContentLength = int64(len(raw))
	resp.Header.Set("Content-Length", strconv.Itoa(len(raw)))
	return nil
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
