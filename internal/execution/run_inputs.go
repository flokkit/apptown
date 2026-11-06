package execution

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// ToolchainSnapshot is written by the caller's install script before its command.
// The runner reads it on the machine that ran the command, as it reads reports.
const ToolchainSnapshot = ".forge-test-toolchain.json"

// RunInputs records observed execution inputs. Nil toolchains are unknown;
// an empty map means mise reported no current tools. Image digests are Docker's
// content-addressed image IDs, read from the containers, never from mutable tags.
type RunInputs struct {
	ImageDigest string              `json:"imageDigest"`
	Toolchain   map[string][]string `json:"toolchain"`
	Services    map[string]string   `json:"services"`
}

func (r Runner) containerImage(ctx context.Context, name string) string {
	out, err := r.docker(ctx, "inspect", "--type", "container", "--format", "{{.Image}}", name)
	digest := strings.TrimSpace(string(out))
	hash, ok := strings.CutPrefix(digest, "sha256:")
	if _, invalid := hex.DecodeString(hash); err != nil || !ok || len(hash) != 64 || invalid != nil {
		return "unknown"
	}
	return digest
}

func (r Runner) captureServiceInputs(ctx context.Context, req Request, inputs *RunInputs) *RunInputs {
	if inputs == nil {
		inputs = &RunInputs{ImageDigest: "unknown"}
	}
	inputs.Services = map[string]string{}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	for _, svc := range req.Services {
		inputs.Services[svc.Name] = r.containerImage(ctx, req.Name+"-"+svc.Name)
	}
	return inputs
}

func readInstalledToolchain(workspace string) map[string][]string {
	raw, err := ReadReport(workspace, ToolchainSnapshot)
	if err != nil {
		return nil
	}
	var entries map[string][]struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(raw, &entries) != nil || entries == nil {
		return nil
	}
	tools := map[string][]string{}
	for name, versions := range entries {
		if len(versions) == 0 {
			tools[name] = []string{"unknown"}
		}
		for _, v := range versions {
			if v.Version == "" {
				tools[name] = append(tools[name], "unknown")
			} else {
				tools[name] = append(tools[name], v.Version)
			}
		}
	}
	return tools
}
