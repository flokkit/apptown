package supervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

const Protocol = 1

// Registration is supplied only by the host administrator, never by a daemon
// request. ConfigurationDigest binds every field except its own digest.
type Registration struct {
	Format              int       `json:"format"`
	Instance            ids.ID    `json:"instance"`
	DataDir             string    `json:"dataDir"`
	StateDir            string    `json:"stateDir"`
	DatabaseIdentity    string    `json:"databaseIdentity"`
	Supervisor          string    `json:"supervisor"`
	SupervisorSHA256    string    `json:"supervisorSHA256"`
	DaemonUID           uint32    `json:"daemonUID"`
	Adapter             string    `json:"adapter"`
	Service             string    `json:"service"`
	UnitSHA256          string    `json:"unitSHA256"`
	EnvironmentFile     string    `json:"environmentFile"`
	EnvironmentSHA256   string    `json:"environmentSHA256"`
	ReleaseDir          string    `json:"releaseDir,omitempty"`
	ExecutableSHA256    string    `json:"executableSHA256,omitempty"`
	Container           string    `json:"container,omitempty"`
	Image               string    `json:"image,omitempty"`
	DockerEngine        string    `json:"dockerEngine,omitempty"`
	DockerSocket        string    `json:"dockerSocket,omitempty"`
	ConfigurationDigest string    `json:"configurationDigest"`
	EnrolledAt          time.Time `json:"enrolledAt"`
	EnrolledByUID       uint32    `json:"enrolledByUID"`
}

var serviceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}\.service$`)
var hexDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func canonical(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/" && !strings.ContainsAny(path, "\x00\r\n")
}
func within(parent, child string) bool {
	return child == parent || strings.HasPrefix(child, parent+string(filepath.Separator))
}

func (r Registration) digest() string {
	r.ConfigurationDigest = ""
	raw, _ := json.Marshal(r) // All fields have total JSON representations after validation.
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (r Registration) validate() error {
	if r.DaemonUID != 0 {
		return errors.New("managed enrollment currently requires the root daemon identity")
	}
	if r.Format != Protocol || r.Instance == ids.Nil || r.EnrolledAt.IsZero() || r.EnrolledByUID != 0 {
		return errors.New("invalid host enrollment identity")
	}
	for _, p := range []string{r.DataDir, r.StateDir, r.Supervisor} {
		if !canonical(p) {
			return errors.New("installation paths must be absolute and canonical")
		}
	}
	if within(r.DataDir, r.StateDir) || within(r.StateDir, r.DataDir) || within(r.DataDir, r.Supervisor) || within(r.StateDir, r.Supervisor) {
		return errors.New("supervisor and state must be outside the replaceable data tree")
	}
	if !hexDigest.MatchString(r.SupervisorSHA256) || !hexDigest.MatchString(r.UnitSHA256) || !serviceName.MatchString(r.Service) {
		return errors.New("invalid registered supervisor or unit identity")
	}
	if !canonical(r.EnvironmentFile) || !hexDigest.MatchString(r.EnvironmentSHA256) || within(r.DataDir, r.EnvironmentFile) || within(r.StateDir, r.EnvironmentFile) {
		return errors.New("registered environment must be separate and content-bound")
	}
	if !hexDigest.MatchString(r.DatabaseIdentity) {
		return errors.New("database identity must be a SHA256 descriptor, never credentials")
	}
	switch r.Adapter {
	case "systemd":
		if !canonical(r.ReleaseDir) || within(r.ReleaseDir, r.StateDir) || within(r.StateDir, r.ReleaseDir) || within(r.ReleaseDir, r.Supervisor) || within(r.DataDir, r.ReleaseDir) || within(r.ReleaseDir, r.DataDir) || !hexDigest.MatchString(r.ExecutableSHA256) {
			return errors.New("invalid separate release identity")
		}
		if r.Container != "" || r.Image != "" || r.DockerEngine != "" || r.DockerSocket != "" {
			return errors.New("systemd registration cannot name a container")
		}
	case "docker":
		if !hexDigest.MatchString(r.Container) || !strings.HasPrefix(r.Image, "sha256:") || !hexDigest.MatchString(strings.TrimPrefix(r.Image, "sha256:")) || r.DockerEngine == "" || !canonical(r.DockerSocket) {
			return errors.New("Docker enrollment needs exact container, image and local engine identity")
		}
		if r.ReleaseDir != "" || r.ExecutableSHA256 != "" {
			return errors.New("container enrollment cannot select a host release")
		}
	default:
		return fmt.Errorf("unsupported managed adapter %q", r.Adapter)
	}
	if r.ConfigurationDigest != r.digest() {
		return errors.New("installation configuration digest differs")
	}
	return nil
}

// SealRegistration binds the host's complete enrollment description. Enrollment
// separately validates paths, permissions, current artifacts and adapter facts.
func SealRegistration(r Registration) (Registration, error) {
	r.ConfigurationDigest = r.digest()
	return r, r.validate()
}
