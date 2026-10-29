package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
)

// DockerSelection is the Docker daemon an AppTown's tasks run on, resolved once
// at startup to the daemon's address. Every call a DockerTasks makes passes
// that address itself (docker --host), so no later change to a context, the
// saved current context or the environment can send a call to another
// daemon: a context is a name the CLI would otherwise look up again on each
// call.
type DockerSelection struct {
	// Host is the daemon's address, as the docker CLI takes it.
	Host string
	// Context is the context Host was read from, empty when DOCKER_HOST
	// named it.
	Context string
	// Refused is why no address could be pinned: the context could not be
	// read, or it needs TLS files a bare address does not carry. A driver
	// with a refused selection creates nothing and observes nothing until
	// AppTown restarts with a selection that resolves.
	Refused error
	// TLSFromEnvironment is whether the CLI would take TLS settings from
	// DOCKER_TLS_VERIFY, DOCKER_TLS and DOCKER_CERT_PATH for this daemon: it
	// does for DOCKER_HOST and the default context, not for a named context,
	// which carries its own. A call passes them on only then.
	TLSFromEnvironment bool
}

// The daemon the CLI's default context uses when DOCKER_HOST is unset:
// the local socket, or with TLS asked for in the environment, the local TLS
// port (as `docker context inspect default` reports).
const (
	defaultDockerHost    = "unix:///var/run/docker.sock"
	defaultDockerTLSHost = "tcp://localhost:2376"
)

// DockerConfigDir is the docker CLI's configuration folder, as the CLI
// finds it: DOCKER_CONFIG, else .docker in the home folder. A home folder
// that cannot be named is an error, never a relative path.
func DockerConfigDir() (string, error) {
	if dir := os.Getenv("DOCKER_CONFIG"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding the Docker configuration: DOCKER_CONFIG is unset and %w", err)
	}
	return filepath.Join(home, ".docker"), nil
}

// SelectDocker resolves the daemon the docker CLI would use now: DOCKER_HOST;
// else DOCKER_CONTEXT; else the current context saved in the CLI's
// configuration (DockerConfigDir); else the default context. It only reads.
// A configuration that is there but cannot be read refuses the selection:
// what it names is not known, and the default daemon is not assumed.
func SelectDocker() DockerSelection {
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		return DockerSelection{Host: host, TLSFromEnvironment: true}
	}
	name := os.Getenv("DOCKER_CONTEXT")
	dir, err := DockerConfigDir()
	if err != nil {
		return DockerSelection{Context: name, Refused: err}
	}
	if name == "" {
		var saved struct {
			CurrentContext string `json:"currentContext"`
		}
		config := filepath.Join(dir, "config.json")
		body, err := os.ReadFile(config)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return DockerSelection{Context: "current", Refused: fmt.Errorf("reading the Docker configuration %s: %w", config, err)}
		case json.Unmarshal(body, &saved) != nil:
			return DockerSelection{Context: "current", Refused: fmt.Errorf("the Docker configuration %s is not JSON", config)}
		default:
			name = saved.CurrentContext
		}
	}
	if name == "" || name == "default" {
		host := defaultDockerHost
		if os.Getenv("DOCKER_TLS_VERIFY") != "" || os.Getenv("DOCKER_TLS") != "" {
			host = defaultDockerTLSHost
		}
		return DockerSelection{Host: host, Context: "default", TLSFromEnvironment: true}
	}
	host, err := dockerContextHost(dir, name)
	return DockerSelection{Host: host, Context: name, Refused: err}
}

// OtherMachine is the address of the machine the daemon runs on when that
// is not this one: the host of a tcp:// or ssh:// address, from DOCKER_HOST
// or a Docker context alike. "" for a local socket (and for a refused
// selection, which names no daemon). It is the one answer to "is Docker on
// this machine", and where published ports answer when it is not.
func (s DockerSelection) OtherMachine() string {
	if s.Refused != nil {
		return ""
	}
	u, err := url.Parse(s.Host)
	if err != nil || (u.Scheme != "tcp" && u.Scheme != "ssh") {
		return ""
	}
	return u.Hostname()
}

// Backend names the selection in task records: the daemon's address, so the
// same daemon reached through DOCKER_HOST or through a context is one
// backend. A refused selection names its context and says it is unresolved;
// it never matches a resolved one.
func (s DockerSelection) Backend() string {
	if s.Refused != nil {
		return "context " + s.Context + " (unresolved)"
	}
	return "host " + s.Host
}

// dockerContextHost reads a named context's daemon address from the CLI's
// own context store under configDir (contexts/meta/<sha256 of the name>/
// meta.json). A context that needs TLS files is refused: --host carries only
// the address, and the files would be looked up again on each call, like the
// name. Such a daemon is selected with DOCKER_HOST and DOCKER_TLS_VERIFY /
// DOCKER_CERT_PATH instead, which AppTown's environment fixes for its
// lifetime.
func dockerContextHost(configDir, name string) (string, error) {
	digest := sha256.Sum256([]byte(name))
	sum := hex.EncodeToString(digest[:])
	body, err := os.ReadFile(filepath.Join(configDir, "contexts", "meta", sum, "meta.json"))
	if err != nil {
		return "", fmt.Errorf("reading Docker context %q: %w", name, err)
	}
	var meta struct {
		Endpoints struct {
			Docker struct {
				Host          string
				SkipTLSVerify bool
			} `json:"docker"`
		}
	}
	if err := json.Unmarshal(body, &meta); err != nil || meta.Endpoints.Docker.Host == "" {
		return "", fmt.Errorf("Docker context %q names no daemon", name)
	}
	tls := filepath.Join(configDir, "contexts", "tls", sum, "docker")
	if _, err := os.Stat(tls); err == nil || !errors.Is(err, os.ErrNotExist) || meta.Endpoints.Docker.SkipTLSVerify {
		return "", fmt.Errorf("Docker context %q uses TLS settings AppTown cannot pin to one daemon; select it with DOCKER_HOST, DOCKER_TLS_VERIFY and DOCKER_CERT_PATH instead", name)
	}
	return meta.Endpoints.Docker.Host, nil
}
