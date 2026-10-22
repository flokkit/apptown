package deployment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/strs"
)

// Hosts reaches Docker daemons: this AppTown's own, and each attached Docker
// host over ssh, with the key its target names written where only AppTown
// reads it. It holds the host settings every boot and copy shares: where
// AppTown keeps its data, its boots' Docker configurations and each
// project's repository. One instance serves Deployment, Preview and every
// other caller; it keeps no state of its own beyond the ssh files it
// writes.
type Hosts struct {
	targets  *Targets
	secrets  *secret.Service
	settings HostSettings
}

// HostSettings are this AppTown's Docker settings.
type HostSettings struct {
	// Scope is this AppTown's own Docker daemon.
	Scope docker.Scope
	// DataDir is AppTown's data directory; SSHConfigPath the ssh config
	// AppTown writes attached hosts' stanzas into ("" is ~/.ssh/config).
	DataDir, SSHConfigPath string
	// BuiltInTarget names the target that is this AppTown's own Docker.
	BuiltInTarget string
	// NoLocalDocker is an AppTown with no local Docker daemon (-no-local-docker):
	// the built-in host is unavailable and releases go to attached Docker
	// hosts only (no_local_docker.go).
	NoLocalDocker bool
}

// NewHosts reaches the targets' Docker daemons with the secrets that hold
// their keys.
func NewHosts(targets *Targets, secrets *secret.Service, settings HostSettings) *Hosts {
	if settings.SSHConfigPath == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			home = "/root"
		}
		settings.SSHConfigPath = filepath.Join(home, ".ssh", "config")
	}
	return &Hosts{targets: targets, secrets: secrets, settings: settings}
}

// Local is this AppTown's own Docker daemon (Absent when it has none).
func (h *Hosts) Local() docker.Scope { return h.settings.Scope }

// NoLocalDocker says the apptown runs with no Docker of its own.
func (h *Hosts) NoLocalDocker() bool { return h.settings.NoLocalDocker }

// BuiltInTarget is the name of the target that is this AppTown's own Docker.
func (h *Hosts) BuiltInTarget() string { return h.settings.BuiltInTarget }

// BootScratch is where this machine's prepared boots keep their own Docker
// configurations; AppTown sweeps it when it starts.
func (h *Hosts) BootScratch() string { return filepath.Join(h.settings.DataDir, "boots") }

// ProjectRepository is AppTown's clone of a project's repository, which
// boots and copies read a selected revision from.
func (h *Hosts) ProjectRepository(slug string) string {
	return filepath.Join(h.settings.DataDir, "workspaces", "project-"+slug)
}

var sshHostRe = regexp.MustCompile(`^ssh://([a-z_][a-z0-9_.-]*)@([A-Za-z0-9.-]+)(?::(\d{1,5}))?$`)

// SSHAddress is a parsed ssh://user@host[:port].
type SSHAddress struct {
	User, Host string
	Port       int
}

func ParseSSHAddress(s string) (SSHAddress, error) {
	m := sshHostRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return SSHAddress{}, fmt.Errorf("the address must look like ssh://user@host or ssh://user@host:port")
	}
	a := SSHAddress{User: m[1], Host: m[2], Port: 22}
	if m[3] != "" {
		n, _ := strconv.Atoi(m[3])
		if n < 1 || n > 65535 {
			return SSHAddress{}, fmt.Errorf("port %s is not a port", m[3])
		}
		a.Port = n
	}
	return a, nil
}

// sshAlias is the Host alias AppTown's ssh config carries for a target. It
// is worked out on every use and never recorded; a block written before 1.0
// under forge-target-<name> is replaced by the next use.
func sshAlias(targetName string) string { return "apptown-target-" + targetName }

// sshStanza renders the config block for one target, between markers the
// AppTown owns so it can replace exactly its own block later. The markers
// keep the words earlier releases wrote, so their blocks are found.
func sshStanza(targetName string, addr SSHAddress, keyPath string) string {
	return fmt.Sprintf("# BEGIN forge target %s\nHost %s\n  HostName %s\n  User %s\n  Port %d\n  IdentityFile %s\n  IdentitiesOnly yes\n  StrictHostKeyChecking accept-new\n  ConnectTimeout 15\n# END forge target %s\n",
		targetName, sshAlias(targetName), addr.Host, addr.User, addr.Port, keyPath, targetName)
}

// upsertSSHStanza writes the target's block into an ssh config file,
// replacing an earlier block for the same target and leaving everything
// else in the file untouched.
func upsertSSHStanza(configPath, targetName, stanza string) error {
	sshFiles.Lock()
	defer sshFiles.Unlock()
	raw, _ := os.ReadFile(configPath)
	begin, end := "# BEGIN forge target "+targetName+"\n", "# END forge target "+targetName+"\n"
	s := string(raw)
	if i := strings.Index(s, begin); i >= 0 {
		if j := strings.Index(s[i:], end); j >= 0 {
			s = s[:i] + s[i+j+len(end):]
		}
	}
	s = strings.TrimRight(s, "\n")
	if s != "" {
		s += "\n\n"
	}
	s += stanza
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return err
	}
	return replaceFile(configPath, []byte(s), 0o600)
}

// sshFiles serializes AppTown's edits of its ssh config and key files:
// every docker call to an attached host prepares them, and the page's
// inventory, its refresh job and a release ask at once.
var sshFiles sync.Mutex

// replaceFile gives path the content data, unless it already has it. The
// new content is written beside it and renamed into place, so an ssh
// started meanwhile reads the old file or the new one, never a truncated
// one: rewriting the config and key in place on every call can leave an ssh
// finding no host stanza or an empty key, and an attached host read as
// unreachable while releases to it work.
func replaceFile(path string, data []byte, perm os.FileMode) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // a no-op once renamed
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// materialiseTargetKey writes the private key held under a secret name to
// a file only AppTown can read, and returns its path. The value never
// reaches a log or a record; the file is under the data directory.
func (h *Hosts) materialiseTargetKey(ctx context.Context, targetName, keyFrom string) (string, error) {
	values, err := h.secrets.WithContext(ctx)
	if err != nil {
		return "", err
	}
	key := values[keyFrom]
	if strings.TrimSpace(key) == "" {
		return "", fmt.Errorf("no secret named %q in AppTown's secrets store (the SSH private key for this host goes there, never in a row)", keyFrom)
	}
	// The file store keeps one line per secret; a key pasted there with
	// its newlines written as \n is unfolded here.
	if !strings.Contains(key, "\n") && strings.Contains(key, `\n`) {
		key = strings.ReplaceAll(key, `\n`, "\n")
	}
	if !strings.Contains(key, "PRIVATE KEY") {
		return "", fmt.Errorf("the secret %q does not look like an SSH private key", keyFrom)
	}
	if !strings.HasSuffix(key, "\n") {
		key += "\n"
	}
	dir := filepath.Join(h.settings.DataDir, "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "target-"+sshKeyFileName(targetName))
	sshFiles.Lock()
	defer sshFiles.Unlock()
	if err := replaceFile(path, []byte(key), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// prepareHostTarget makes the target reachable — key on disk, ssh stanza
// in place — and returns the DOCKER_HOST to use. It is what a check and an
// attachment both do first.
func (h *Hosts) prepareHostTarget(ctx context.Context, name string, addr SSHAddress, keyFrom string) (string, error) {
	keyPath, err := h.materialiseTargetKey(ctx, name, keyFrom)
	if err != nil {
		return "", err
	}
	if err := upsertSSHStanza(h.settings.SSHConfigPath, name, sshStanza(name, addr, keyPath)); err != nil {
		return "", fmt.Errorf("writing AppTown's ssh config: %w", err)
	}
	return "ssh://" + sshAlias(name), nil
}

// ProbeHostTarget asks the remote daemon to answer. ok and words.
func (h *Hosts) ProbeHostTarget(ctx context.Context, name string, addr SSHAddress, keyFrom string) (bool, string) {
	dockerHost, err := h.prepareHostTarget(ctx, name, addr, keyFrom)
	if err != nil {
		return false, err.Error()
	}
	out, err := h.settings.Scope.WithHost(dockerHost, "", "").Run(ctx, "info", "--format", "{{.ServerVersion}} on {{.OperatingSystem}}, {{.ContainersRunning}}/{{.Containers}} containers")
	if err != nil {
		return false, fmt.Sprintf("%s did not answer as a Docker daemon over ssh: %s", addr.Host, strs.Truncate(strings.TrimSpace(string(out)), 200))
	}
	return true, "docker " + strings.TrimSpace(string(out))
}

// HostTargetAddress reads an attached Docker host's address from its row.
func HostTargetAddress(t *Target) (SSHAddress, string, error) {
	host, _ := t.Address["host"].(string)
	keyFrom, _ := t.Address["keyFrom"].(string)
	addr, err := ParseSSHAddress(host)
	if err != nil {
		return SSHAddress{}, "", err
	}
	if keyFrom == "" {
		return SSHAddress{}, "", fmt.Errorf("the target names no secret for its key")
	}
	return addr, keyFrom, nil
}

// sshKeyFileName is the key file's name for a target: a digest, so a
// target's name never shapes a path.
func sshKeyFileName(targetName string) string {
	sum := sha256.Sum256([]byte(targetName))
	return hex.EncodeToString(sum[:6]) + ".key"
}

// ErrNotDockerHost is a Docker daemon asked of a target that is not a
// Docker host (App Platform, Portainer, a bucket).
var ErrNotDockerHost = errors.New("deployment requires a Docker host target")

// Deployment execution uses its selected target, and returns configuration
// errors instead of falling back to another daemon.
func (h *Hosts) DockerScope(ctx context.Context, t *Target) (docker.Scope, error) {
	if err := ctx.Err(); err != nil {
		return docker.Scope{}, err
	}
	if t == nil || t.Kind != KindDockerHost {
		return docker.Scope{}, ErrNotDockerHost
	}
	if t.Origin == OriginBuiltIn && h.settings.NoLocalDocker {
		return docker.Scope{}, ErrNoLocalDocker
	}
	if t.Origin == OriginBuiltIn {
		return h.settings.Scope, nil
	}
	if t.Origin != OriginAttached {
		return docker.Scope{}, fmt.Errorf("Docker target %s has no supported connection", t.Name)
	}
	addr, keyFrom, err := HostTargetAddress(t)
	if err != nil {
		return docker.Scope{}, err
	}
	host, err := h.prepareHostTarget(ctx, t.Name, addr, keyFrom)
	if err != nil {
		return docker.Scope{}, err
	}
	door := attachedFrontDoor(t, addr.Host)
	return h.settings.Scope.WithHost(host, door.Hostname, door.Domain), nil
}

// DockerScopeFor resolves exactly the recorded target. Missing identity or
// unusable credentials are errors, never a choice of a different daemon.
func (h *Hosts) DockerScopeFor(ctx context.Context, targetID ids.ID) (docker.Scope, error) {
	t, err := h.targets.ByID(ctx, targetID)
	if err != nil {
		return docker.Scope{}, fmt.Errorf("resolving Docker target %d: %w", targetID, err)
	}
	return h.DockerScope(ctx, t)
}

// Watched are the deployments the apptown runs on Docker hosts it talks to
// directly: settled Docker-host deployments, never previews or operations
// still running or uncertain.
func (h *Hosts) Watched(ctx context.Context, deployments *Deployments) []docker.Watched {
	var out []docker.Watched
	records, err := deployments.StandingWithoutOpenAttempt(ctx)
	if _, err := database.Lookup(err); err != nil {
		log.Printf("read failed: %v", err)
	}
	for _, e := range records {
		target, err := h.targets.ByID(ctx, e.TargetID)
		if err != nil {
			log.Printf("health controller: %s/%s: resolving Docker target %d: %v", e.Project, e.Name, e.TargetID, err)
			continue
		}
		if target.Kind != KindDockerHost {
			continue // other providers own their workloads' health
		}
		dk, err := h.DockerScope(ctx, target)
		if err != nil {
			log.Printf("health controller: %s/%s: %v", e.Project, e.Name, err)
			continue
		}
		out = append(out, docker.Watched{DeploymentID: e.ID, ProjectID: e.ProjectID, Identity: deploy.DockerIdentity{Deployment: e.Label(), Generation: e.ObservedGeneration}, Slug: e.Project, Name: e.Name, HostPort: e.HostPort, Container: e.Container, Docker: dk})
	}
	return out
}
