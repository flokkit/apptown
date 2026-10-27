package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/execution"
)

// Host observes only the service/container captured by host enrollment. Run is
// an assembly test seam; request envelopes never supply command arguments.
type Host struct {
	Run func(context.Context, []string) ([]byte, error)
}

func (h Host) run(ctx context.Context, args ...string) ([]byte, error) {
	return h.runFor(ctx, 10*time.Second, args...)
}
func (h Host) runFor(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	if h.Run != nil {
		return h.Run(ctx, args)
	}
	out := (execution.Runner{}).Run(ctx, execution.Local, execution.Request{Argv: args, Timeout: timeout})
	return out.Output, out.RunErr
}

func hashFile(path string) (string, error) {
	if err := secureArtifact(path); err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func compareFile(path, want string) error {
	got, err := hashFile(path)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("registered artifact differs: %s", path)
	}
	return nil
}
func (h Host) unit(ctx context.Context, r Registration) (map[string]string, error) {
	raw, err := h.run(ctx, "systemctl", "show", "--no-pager", "--property=MainPID,ActiveState,SubState,FragmentPath,DropInPaths,NeedDaemonReload,LoadState", r.Service)
	if err != nil {
		return nil, err
	}
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, errors.New("invalid systemd observation")
		}
		fields[key] = value
	}
	if len(fields) != 7 || fields["NeedDaemonReload"] != "no" || fields["LoadState"] != "loaded" || fields["FragmentPath"] == "" || fields["DropInPaths"] != "" {
		return nil, errors.New("registered unit missing or has unregistered drop-ins")
	}
	if err := compareFile(fields["FragmentPath"], r.UnitSHA256); err != nil {
		return nil, err
	}
	if err := compareFile(r.EnvironmentFile, r.EnvironmentSHA256); err != nil {
		return nil, err
	}
	if err := compareFile(r.Supervisor, r.SupervisorSHA256); err != nil {
		return nil, err
	}
	return fields, nil
}
func (h Host) Observe(ctx context.Context, r Registration) (Observation, error) {
	unit, err := h.unit(ctx, r)
	if err != nil {
		return Observation{}, err
	}
	switch r.Adapter {
	case "systemd":
		if err := compareFile(filepath.Join(r.ReleaseDir, "bin", "apptownd"), r.ExecutableSHA256); err != nil {
			return Observation{}, err
		}
		pid, err := strconv.Atoi(unit["MainPID"])
		if err != nil {
			return Observation{}, err
		}
		if pid == 0 {
			if unit["ActiveState"] != "inactive" && unit["ActiveState"] != "failed" {
				return Observation{}, errors.New("service state is unsettled")
			}
			return Observation{}, nil
		}
		if unit["ActiveState"] != "active" && unit["ActiveState"] != "activating" && unit["ActiveState"] != "deactivating" {
			return Observation{}, errors.New("service process has inconsistent state")
		}
		return managedChild(pid, r)
	case "docker":
		return h.docker(ctx, r)
	default:
		return Observation{}, errors.New("unsupported registered adapter")
	}
}
func (h Host) docker(ctx context.Context, r Registration) (Observation, error) {
	host := "unix://" + r.DockerSocket
	raw, err := h.run(ctx, "docker", "--host", host, "info", "--format", "{{.ID}}")
	if err != nil {
		return Observation{}, err
	}
	if strings.TrimSpace(string(raw)) != r.DockerEngine {
		return Observation{}, errors.New("Docker engine identity changed")
	}
	raw, err = h.run(ctx, "docker", "--host", host, "inspect", r.Container)
	if err != nil {
		return Observation{}, err
	}
	var rows []struct {
		ID     string `json:"Id"`
		Image  string
		Config struct{ Env []string }
		State  struct {
			Running bool
			Pid     int
			Status  string
		}
		HostConfig struct {
			RestartPolicy struct{ Name string }
			NetworkMode   string
		}
		Mounts []struct{ Type, Source, Destination string }
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return Observation{}, err
	}
	if len(rows) != 1 {
		return Observation{}, errors.New("Docker inspect did not return exact container")
	}
	c := rows[0]
	if err := effectiveEnvironment(r, c.Config.Env); err != nil {
		return Observation{}, err
	}
	if c.ID != r.Container || c.Image != r.Image || c.HostConfig.RestartPolicy.Name != "no" || c.HostConfig.NetworkMode != "host" {
		return Observation{}, errors.New("registered container identity or restart policy changed")
	}
	data, control := false, false
	for _, m := range c.Mounts {
		if m.Destination == r.DataDir {
			data = m.Type == "bind" && m.Source == r.DataDir
		}
		if m.Destination == r.StateDir {
			control = m.Type == "bind" && m.Source == r.StateDir
		}
	}
	if !data || !control {
		return Observation{}, errors.New("registered data/control mounts changed")
	}
	if !c.State.Running {
		if c.State.Pid != 0 || (c.State.Status != "created" && c.State.Status != "exited") {
			return Observation{}, errors.New("container state is unsettled")
		}
		return Observation{}, nil
	}
	if c.State.Pid <= 0 {
		return Observation{}, errors.New("running container lacks host process identity")
	}
	uid, err := processUID(c.State.Pid)
	if err != nil {
		return Observation{}, err
	}
	if uid != r.DaemonUID {
		return Observation{}, errors.New("registered container process user changed")
	}
	return Observation{PID: c.State.Pid, UID: uid, Running: true}, nil
}
