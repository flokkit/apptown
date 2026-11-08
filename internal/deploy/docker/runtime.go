package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/dockerlabel"
)

// What the host says about a container: identity and liveness, never
// Config.Env. The boot proof is recorded on the attempt and verified on
// resume, so a restart never invents a fresh healthy probe.

type Runtime struct {
	ID    string `json:"id"`
	Image string `json:"image"`
	// ImageName is the image as the container was created from it
	// (Config.Image), e.g. registry/app:tag; Image is its ID.
	ImageName string            `json:"imageName"`
	Running   bool              `json:"running"`
	Labels    map[string]string `json:"labels"`
	// MemoryLimit is the memory the container is held to, in bytes; 0 is no
	// limit.
	MemoryLimit int64 `json:"memoryLimit"`
	// NanoCPUs is the CPU the container is held to, in billionths of a
	// core; 0 is no limit.
	NanoCPUs int64 `json:"nanoCpus"`
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
const RuntimeFormat = `{"id":{{json .Id}},"image":{{json .Image}},"imageName":{{json .Config.Image}},"nanoCpus":{{json .HostConfig.NanoCpus}},"running":{{json .State.Running}},"labels":{{json .Config.Labels}},"memoryLimit":{{json .HostConfig.Memory}},` +
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
func verifyIdentity(id deploy.DockerIdentity, runtime Runtime, image string) error {
	if !runtime.Running {
		return errors.New("the selected deployment container is not running")
	}
	return verifyContainerIdentity(id, runtime, image)
}
func verifyContainerIdentity(id deploy.DockerIdentity, runtime Runtime, image string) error {
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
	return nil
}

func recordBoot(ctx context.Context, dk Scope, id deploy.DockerIdentity, name, image, url string, port int, stopped bool, checkpoint deploy.Checkpoint) (deploy.Observation, error) {
	runtime, err := Inspect(ctx, dk, name)
	if err != nil {
		return deploy.Observation{}, err
	}
	if runtime.Running == stopped {
		return deploy.Observation{}, errors.New("application runtime differs from requested state")
	}
	if err := verifyContainerIdentity(id, runtime, image); err != nil {
		return deploy.Observation{}, err
	}
	if err := ctx.Err(); err != nil {
		return deploy.Observation{}, err
	}
	o := deploy.Observation{Generation: id.Generation, ImageDigest: runtime.Image, Rollout: "active", Health: "healthy", URL: url,
		Handle: map[string]any{"container": name, "hostPort": port}}
	if stopped {
		o.Rollout, o.Health = "stopped", "unknown"
	}
	if err := checkpoint(map[string]any{"dockerReady": o}); err != nil {
		return deploy.Observation{}, fmt.Errorf("recording Docker boot proof: %w", err)
	}
	return o, nil
}

// resume observes the admitted container identity even when AppTown stopped
// before recording readiness. An absent container or older generation permits a retry;
// a matching running generation is up, with current health left unknown.
func resume(ctx context.Context, dk Scope, p deploy.Placement, generation int64, noted map[string]any) (deploy.Observation, error) {
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
	if proof.Container == "" || proof.Identity.Deployment != p.Label || proof.Identity.Generation != generation || proof.Identity.Artifact == "" {
		return deploy.Observation{}, errors.New("Docker apply identity is missing or differs; its outcome is uncertain — inspect the host before trying again")
	}
	if proof.Ready.Generation != 0 {
		handle, ok := proof.Ready.Handle.(map[string]any)
		if !ok || handle["container"] != proof.Container || proof.Ready.Generation != generation {
			return deploy.Observation{}, errors.New("the attempt's boot record differs from its admitted container")
		}
	}
	runtime, err := Inspect(ctx, dk, proof.Container)
	if errors.Is(err, ErrContainerAbsent) {
		// The daemon answered: the container the attempt booted is gone,
		// so the deployment does not stand. That is a known outcome.
		return deploy.Observation{}, fmt.Errorf("the container %s is gone: %w", proof.Container, deploy.ErrNotApplied)
	}
	if err != nil {
		return deploy.Observation{}, err // the host could not be asked
	}
	if runtime.Labels[dockerlabel.Deployment] == string(proof.Identity.Deployment) && runtime.Labels[dockerlabel.Generation] != fmt.Sprint(generation) {
		if seen, err := strconv.ParseInt(runtime.Labels[dockerlabel.Generation], 10, 64); err == nil && seen < generation {
			return deploy.Observation{}, deploy.ErrNotApplied
		}
		return deploy.Observation{}, deploy.Terminal(errors.New("the provider holds a different generation"))
	}
	if runtime.Running == p.Stopped {
		if proof.Ready.Generation == 0 {
			return deploy.Observation{}, deploy.ErrNotApplied
		}
		return deploy.Observation{}, deploy.Terminal(errors.New("application runtime differs from the recorded boot"))
	}
	image := proof.Ready.ImageDigest
	if image == "" {
		image = runtime.Image
	}
	if err := verifyContainerIdentity(proof.Identity, runtime, image); err != nil {
		return deploy.Observation{}, deploy.Terminal(err)
	}
	if proof.Ready.Generation == 0 {
		proof.Ready = deploy.Observation{Generation: generation, ImageDigest: runtime.Image, Rollout: "active", URL: p.URL, Handle: map[string]any{"container": proof.Container, "hostPort": p.HostPort}}
		if p.Stopped {
			proof.Ready.Rollout = "stopped"
		}
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
