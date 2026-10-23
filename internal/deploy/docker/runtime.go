package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
)

// What the host says about a container: identity and liveness, never
// Config.Env. The boot proof is recorded on the attempt and verified on
// resume, so a restart never invents a fresh healthy probe.

type Runtime struct {
	ID      string            `json:"id"`
	Image   string            `json:"image"`
	Running bool              `json:"running"`
	Labels  map[string]string `json:"labels"`
	// MemoryLimit is the memory the container is held to, in bytes; 0 is no
	// limit.
	MemoryLimit int64 `json:"memoryLimit"`
	// State is what the daemon says of the container's process: nil when
	// the answer carried none, which is "not reported", never a clean exit.
	State *ContainerState `json:"state,omitempty"`
}

// ContainerState is the daemon's own record of a container's process: its
// status, its last exit, whether the kernel killed it at its memory limit,
// Docker's restart counter for this container (not a lifetime count across
// containers) and when it last started and finished. Health is the
// container's Docker health check status, "" when it has none configured.
type ContainerState struct {
	Status       string    `json:"status"`
	ExitCode     int       `json:"exitCode"`
	OOMKilled    bool      `json:"oomKilled"`
	RestartCount int       `json:"restartCount"`
	StartedAt    time.Time `json:"startedAt"`
	FinishedAt   time.Time `json:"finishedAt"`
	Health       string    `json:"health"`
}

// RuntimeFormat is the inspect format the host is asked for: identity,
// liveness, the memory limit and the process state, never Config.Env or
// the health check's output. Docker renders it with missingkey=error and
// keeps State.Health only for a container with a health check, so Health is
// read with index, which answers nothing for a missing key. The process
// runner cancels the CLI and its children within a bounded deadline.
const RuntimeFormat = `{"id":{{json .Id}},"image":{{json .Image}},"running":{{json .State.Running}},"labels":{{json .Config.Labels}},"memoryLimit":{{json .HostConfig.Memory}},` +
	`"state":{"status":{{json .State.Status}},"exitCode":{{json .State.ExitCode}},"oomKilled":{{json .State.OOMKilled}},"restartCount":{{json .RestartCount}},` +
	`"startedAt":{{json .State.StartedAt}},"finishedAt":{{json .State.FinishedAt}},"health":{{with index .State "Health"}}{{json .Status}}{{else}}""{{end}}}}`

var ErrContainerAbsent = errors.New("Docker container is absent")

func Inspect(ctx context.Context, dk Scope, name string) (Runtime, error) {
	out, err := dk.Command(ctx, Command{Args: []string{"inspect", "--format", RuntimeFormat, name}, Timeout: 30 * time.Second})
	if ctx.Err() != nil {
		return Runtime{}, fmt.Errorf("Docker inspection interrupted: %w", ctx.Err())
	}
	return decodeRuntime(out, err, name)
}

func decodeRuntime(output []byte, err error, name string) (Runtime, error) {
	if err != nil {
		for _, line := range strings.Split(string(output), "\n") {
			line = strings.TrimSpace(line)
			lower := strings.ToLower(line)
			if strings.HasSuffix(line, name) && (strings.HasSuffix(lower, ": no such object: "+strings.ToLower(name)) || strings.HasSuffix(lower, ": no such container: "+strings.ToLower(name))) {
				return Runtime{}, fmt.Errorf("%w: %s", ErrContainerAbsent, name)
			}
		}
		return Runtime{}, fmt.Errorf("inspecting deployment container: %w", err)
	}
	var runtime Runtime
	if err := json.Unmarshal(output, &runtime); err != nil {
		return runtime, fmt.Errorf("reading deployment runtime identity: %w", err)
	}
	return runtime, nil
}

// verifyDockerIdentity checks that a running container is the one the
// deployment was admitted for: labels, image and liveness.
func VerifyIdentity(id deploy.DockerIdentity, runtime Runtime, image string) error {
	if id.Deployment == "" || id.Generation <= 0 || id.Artifact == "" {
		return errors.New("deployment has incomplete Docker identity")
	}
	carried := runtime.Labels
	for label, value := range id.Labels() {
		if carried[label] != value {
			return fmt.Errorf("container does not belong to the selected deployment (%s differs)", label)
		}
	}
	if image == "" || runtime.Image != image {
		return errors.New("container image differs from the selected deployment image")
	}
	if !runtime.Running {
		return errors.New("the selected deployment container is not running")
	}
	return nil
}

func RecordBoot(ctx context.Context, dk Scope, id deploy.DockerIdentity, name, image, url string, port int, checkpoint deploy.Checkpoint) (deploy.Observation, error) {
	runtime, err := Inspect(ctx, dk, name)
	if err != nil {
		return deploy.Observation{}, err
	}
	if err := VerifyIdentity(id, runtime, image); err != nil {
		return deploy.Observation{}, err
	}
	if err := ctx.Err(); err != nil {
		return deploy.Observation{}, err
	}
	o := deploy.Observation{Generation: id.Generation, ImageDigest: runtime.Image, Rollout: "active", Health: "healthy", URL: url,
		Handle: map[string]any{"container": name, "hostPort": port}}
	if err := checkpoint(map[string]any{"dockerReady": o}); err != nil {
		return deploy.Observation{}, fmt.Errorf("recording Docker boot proof: %w", err)
	}
	return o, nil
}

// Resume settles an attempt AppTown lost track of from what it recorded and
// what the host says now. Whenever the host answers, the outcome is known:
// the container the attempt recorded booting healthy, running as recorded,
// is the deployment; anything else (no record of a healthy boot, the
// container gone, replaced or stopped) is a failed attempt, whose leftovers
// the next release or a take-down removes. Only a host that cannot be asked
// leaves it unknown, for a later look. An attempt with no record whose boot
// did come up (AppTown stopped between the boot and writing its record)
// reads failed while its container runs: an accepted cost.
func Resume(ctx context.Context, dk Scope, p deploy.Placement, generation int64, noted map[string]any) (deploy.Observation, error) {
	if err := ctx.Err(); err != nil {
		return deploy.Observation{}, err
	}
	var proof struct {
		Container string                `json:"container"`
		Identity  deploy.DockerIdentity `json:"docker"`
		Ready     deploy.Observation    `json:"dockerReady"`
	}
	raw, err := json.Marshal(noted)
	if err == nil {
		err = json.Unmarshal(raw, &proof)
	}
	if err != nil {
		return deploy.Observation{}, deploy.Terminal(fmt.Errorf("the attempt's record cannot be read: %w", err))
	}
	if proof.Container == "" || proof.Identity.Deployment != p.Label || proof.Identity.Generation != generation || proof.Ready.Generation != generation || proof.Ready.Rollout != "active" {
		return deploy.Observation{}, deploy.Terminal(errors.New("this attempt never recorded that its new version came up, so it did not finish"))
	}
	handle, ok := proof.Ready.Handle.(map[string]any)
	if !ok || handle["container"] != proof.Container {
		return deploy.Observation{}, deploy.Terminal(errors.New("the attempt's record names a different container than the one it started"))
	}
	runtime, err := Inspect(ctx, dk, proof.Container)
	if errors.Is(err, ErrContainerAbsent) {
		// The daemon answered: the container the attempt booted is gone,
		// so the deployment does not stand. That is a known outcome.
		return deploy.Observation{}, deploy.Terminal(fmt.Errorf("the container %s the attempt booted is gone", proof.Container))
	}
	if err != nil {
		return deploy.Observation{}, err // the host could not be asked
	}
	if err := VerifyIdentity(proof.Identity, runtime, proof.Ready.ImageDigest); err != nil {
		return deploy.Observation{}, deploy.Terminal(err)
	}
	// Successful cold boot is recorded evidence; current health belongs to the
	// health controller. Running alone must not invent a fresh healthy probe.
	proof.Ready.Health = "unknown"
	return proof.Ready, nil
}

// BrowserValuesFormat asks the host for a container's immutable id and the one
// reserved browser value it was started with, and nothing else of its
// environment: every other variable may hold a secret, which is why Inspect
// never reads Config.Env.
const BrowserValuesFormat = `{"id":{{json .Id}},"env":[{{range .Config.Env}}{{if eq (index (split . "=") 0) "` + BrowserEnvVar + `"}}{{json .}},{{end}}{{end}}null]}`

// InstalledBrowser is what a running static part was started with for
// /_apptown/env.js, read from the container itself.
type InstalledBrowser struct {
	ID     string
	Values map[string]string
}

// ErrNoBrowserValues is a container that carries no readable browser value:
// a server part, or one started before AppTown set it. Either way what its
// page is given is not known.
var ErrNoBrowserValues = errors.New("the container carries no readable browser values")

// InspectBrowserValues reads the browser values a container was started
// with. A missing, repeated or malformed value is ErrNoBrowserValues, never
// an empty set: an empty set is a fact the container must state.
func InspectBrowserValues(ctx context.Context, dk Scope, name string) (InstalledBrowser, error) {
	out, err := dk.Command(ctx, Command{Args: []string{"inspect", "--format", BrowserValuesFormat, name}, Timeout: 30 * time.Second})
	if ctx.Err() != nil {
		return InstalledBrowser{}, fmt.Errorf("Docker inspection interrupted: %w", ctx.Err())
	}
	if err != nil {
		_, err = decodeRuntime(out, err, name)
		return InstalledBrowser{}, err
	}
	var raw struct {
		ID  string    `json:"id"`
		Env []*string `json:"env"`
	}
	if err := json.Unmarshal(out, &raw); err != nil || raw.ID == "" {
		return InstalledBrowser{}, fmt.Errorf("reading the container's browser values: %w", ErrNoBrowserValues)
	}
	var found []string
	for _, kv := range raw.Env {
		if kv != nil {
			found = append(found, strings.TrimPrefix(*kv, BrowserEnvVar+"="))
		}
	}
	values := map[string]string{}
	if len(found) != 1 || json.Unmarshal([]byte(found[0]), &values) != nil || values == nil {
		return InstalledBrowser{ID: raw.ID}, ErrNoBrowserValues
	}
	return InstalledBrowser{ID: raw.ID, Values: values}, nil
}
