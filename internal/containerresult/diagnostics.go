package containerresult

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// evidence is what Docker shows of the probe container name, its image
// and its own versions, in one line, for a read that failed or whose file
// and log disagree: the fields that say which image and command ran, how
// its output was handled and how it ended, never its environment. A part
// that could not be read says so.
func evidence(ctx context.Context, dk Docker, name string) string {
	var parts []string
	if logs, err := dk(ctx, nil, "logs", name); err != nil {
		parts = append(parts, "container log unavailable: "+err.Error())
	} else {
		parts = append(parts, fmt.Sprintf("container log %q", clip(string(logs))))
	}
	var c struct {
		Image   string
		Path    string
		Args    []string
		LogPath string
		Config  struct {
			Image                           string
			Entrypoint, Cmd                 []string
			AttachStdout, AttachStderr, Tty bool
		}
		Mounts []struct {
			Source, Destination string
			RW                  bool
		}
		HostConfig struct{ LogConfig struct{ Type string } }
		State      struct {
			Status                string
			ExitCode              int
			Error                 string
			OOMKilled             bool
			StartedAt, FinishedAt string
		}
	}
	if err := inspectJSON(ctx, dk, &c, "inspect", "--type", "container", "--format", "{{json .}}", name); err != nil {
		parts = append(parts, "container inspect: "+err.Error())
	} else {
		var mounts []string
		for _, m := range c.Mounts {
			mounts = append(mounts, fmt.Sprintf("%s:%s rw=%v", m.Source, m.Destination, m.RW))
		}
		parts = append(parts, fmt.Sprintf("container image %s (%s), path %s, args %q, entrypoint %q, cmd %q, attach stdout %v stderr %v tty %v, mounts [%s], log %s at %s, state %s exit %d error %q oom %v started %s finished %s",
			c.Image, c.Config.Image, c.Path, clip(strings.Join(c.Args, " ")), c.Config.Entrypoint, clip(strings.Join(c.Config.Cmd, " ")),
			c.Config.AttachStdout, c.Config.AttachStderr, c.Config.Tty, strings.Join(mounts, "; "),
			c.HostConfig.LogConfig.Type, c.LogPath, c.State.Status, c.State.ExitCode, c.State.Error, c.State.OOMKilled, c.State.StartedAt, c.State.FinishedAt))
		var img struct {
			ID           string `json:"Id"`
			RepoDigests  []string
			Os           string
			Architecture string
			Config       struct{ Entrypoint, Cmd []string }
		}
		if c.Image == "" {
			parts = append(parts, "image inspect: the container names no image")
		} else if err := inspectJSON(ctx, dk, &img, "image", "inspect", "--format", "{{json .}}", c.Image); err != nil {
			parts = append(parts, "image inspect: "+err.Error())
		} else {
			parts = append(parts, fmt.Sprintf("image %s digests %q %s/%s entrypoint %q cmd %q", img.ID, img.RepoDigests, img.Os, img.Architecture, img.Config.Entrypoint, img.Config.Cmd))
		}
	}
	var v struct {
		Client struct{ Version string }
		Server struct {
			Version    string
			Components []struct{ Name, Version string }
		}
	}
	if err := inspectJSON(ctx, dk, &v, "version", "--format", "{{json .}}"); err != nil {
		parts = append(parts, "docker version: "+err.Error())
	} else {
		var comps []string
		for _, c := range v.Server.Components {
			comps = append(comps, c.Name+" "+c.Version)
		}
		parts = append(parts, fmt.Sprintf("docker client %s, server %s (%s)", v.Client.Version, v.Server.Version, strings.Join(comps, ", ")))
	}
	return strings.Join(parts, "; ")
}

// inspectJSON runs a docker call that prints one JSON object and decodes
// it into v, from its first brace (a caller's combined output may lead
// with a notice).
func inspectJSON(ctx context.Context, dk Docker, v any, args ...string) error {
	// What Docker printed is never kept here: it may hold the container's
	// environment, which the fields chosen above leave out.
	raw, err := dk(ctx, nil, args...)
	out := string(raw)
	if err != nil {
		return err
	}
	i := strings.Index(out, "{")
	if i < 0 {
		return errors.New("no JSON in its answer")
	}
	if err := json.NewDecoder(strings.NewReader(out[i:])).Decode(v); err != nil {
		return fmt.Errorf("its answer could not be read: %v", err)
	}
	return nil
}
