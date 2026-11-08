package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/dockerlabel"
)

var _ deploy.FleetObserver = Inventory{}

// ObserveAll asks for all recorded containers in one command. Docker reports
// absent names separately; a failed host read makes the whole reading unknown.
// A container with an invalid generation has an empty observation, so the
// caller's generation check rejects only that deployment.
// Inspect only the runtime fields, never container environment or probe output.
func (i Inventory) ObserveAll(ctx context.Context, ps []deploy.Placement) ([]deploy.Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	args := []string{"inspect", "--format", `{{json .Name}}` + "\t" + RuntimeFormat}
	asked := map[string]bool{}
	for _, p := range ps {
		if p.Container != "" && !asked[p.Container] {
			args = append(args, p.Container)
			asked[p.Container] = true
		}
	}
	found := map[string]*Runtime{}
	if len(asked) > 0 {
		dk, err := i.Scope(ctx)
		if err != nil {
			return nil, err
		}
		var stdout, stderr bytes.Buffer
		_, runErr := dk.Command(ctx, Command{Args: args, Stdout: &stdout, Stderr: &stderr})
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
			if line == "" {
				continue
			}
			nameJSON, raw, ok := strings.Cut(line, "\t")
			var name string
			var runtime Runtime
			if !ok || json.Unmarshal([]byte(nameJSON), &name) != nil || json.Unmarshal([]byte(raw), &runtime) != nil || name == "" || runtime.ID == "" {
				return nil, fmt.Errorf("could not read the host's container reading")
			}
			found[strings.TrimPrefix(name, "/")] = &runtime
			found[runtime.ID] = &runtime
		}
		missing := map[string]bool{}
		for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
			if line == "" {
				continue
			}
			matched := false
			for name := range asked {
				if _, err := decodeRuntime([]byte(line), fmt.Errorf("docker inspect failed"), name); errors.Is(err, ErrContainerAbsent) {
					missing[name], matched = true, true
					break
				}
			}
			if !matched {
				return nil, fmt.Errorf("could not read the host's containers: %s", line)
			}
		}
		if runErr != nil && len(missing) == 0 {
			return nil, fmt.Errorf("reading the host's containers: %w", runErr)
		}
		for name := range asked {
			if found[name] == nil && !missing[name] {
				return nil, fmt.Errorf("the host did not report container %s", name)
			}
		}
	}
	out := make([]deploy.Observation, len(ps))
	for n, p := range ps {
		o, err := containerObservation(p, found[p.Container])
		if err != nil {
			// Container identity is not a host transport failure. The empty
			// answer cannot establish a generation or a runtime state.
			continue
		}
		out[n] = o
	}
	return out, nil
}

func containerObservation(p deploy.Placement, runtime *Runtime) (deploy.Observation, error) {
	o := deploy.Observation{Generation: p.Generation, URL: p.URL, Handle: p.Handle, Rollout: "absent", Health: "unknown"}
	if runtime == nil {
		return o, nil
	}
	generation, err := strconv.ParseInt(runtime.Labels[dockerlabel.Generation], 10, 64)
	if err != nil || generation <= 0 {
		return deploy.Observation{}, fmt.Errorf("the host's container has no valid generation label")
	}
	o.Generation = generation
	if runtime.Running {
		o.Rollout, o.Health = "active", "healthy"
	} else {
		o.Health = "unhealthy"
		if p.Stopped {
			o.Rollout, o.Health = "stopped", "unknown"
		}
	}
	return o, nil
}
