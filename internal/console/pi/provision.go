package pi

import (
	"context"
	"flokkit.io/forge/internal/dockerlabel"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/dind"
)

const (
	tsPiHeader  = "2006-01-02T15:04:05.000Z" // pi session-file header timestamp
	tsFileStamp = "2006-01-02T15-04-05"      // session filename stamp
)

// writableDirs are the runtime working dirs the container may write; the
// session record and the launcher files stay server-owned.
func writableDirs() []string {
	return []string{"pi-home", "sessions", "scratch", "uploads", "docker-client"}
}

// provision (re)generates a session's runtime surface — the launcher, the
// tmux conf, the pinned session file — and creates its container from the
// CURRENT agent image. Called at creation and again when what runs went
// stale: sessions persist, runtimes must not. The conversation (session
// files, steering, scratch) lives in the mounted /console dir and survives.
func (r *Runtime) provision(in console.Launch) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	// The chat must open clean: pre-create the pinned session file so pi
	// FINDS it instead of warning it into existence. The header is pi's own
	// minimal record (version 3); if pi's format moves on, the worst case is
	// the old warning returning, not breakage.
	sessDir := filepath.Join(in.Dir, "sessions")
	if entries, err := os.ReadDir(sessDir); err == nil && len(entries) == 0 {
		now := r.cfg.Now().UTC()
		header := fmt.Sprintf(`{"type":"session","version":3,"id":%q,"timestamp":"%s","cwd":%q}`, in.AgentSession, now.Format(tsPiHeader), console.WorkMount)
		_ = os.WriteFile(filepath.Join(sessDir, now.Format(tsFileStamp)+"-000Z_"+in.AgentSession+".jsonl"), []byte(header+"\n"), 0o644)
	}
	// Context files stay off (what steers a session is exactly the steering
	// file); skills and extensions load only through the staged home.
	piCmd := "exec pi --session-id '" + in.AgentSession + "' --session-dir " + console.SessionsMount +
		" --append-system-prompt " + console.Mount + "/" + console.SteeringFile + " --no-context-files"
	if in.Model != "" {
		piCmd += " --model '" + strings.ReplaceAll(in.Model, "'", "") + "'"
	}
	prelude := console.ToolchainPrelude + "\n"
	if t := r.cfg.LocalTLS; t != nil {
		if err := os.WriteFile(filepath.Join(in.Dir, console.LocalCAFile), t.CACert, 0o644); err != nil {
			return err
		}
		prelude += console.LocalTLSPrelude(*t) + "\n"
	}
	if err := os.WriteFile(filepath.Join(in.Dir, "run-pi.sh"), []byte("#!/bin/sh\ncd "+console.WorkMount+"\n"+prelude+piCmd+"\n"), 0o755); err != nil {
		return err
	}
	// status off: pi owns the screen; escape-time: don't lag ESC in the TUI.
	tmuxConf := "set -g status off\nset -sg escape-time 10\nset -g history-limit 10000\nset -g extended-keys on\n"
	if err := os.WriteFile(filepath.Join(in.Dir, "tmux.conf"), []byte(tmuxConf), 0o644); err != nil {
		return err
	}
	work := in.Workspace.Path + ":" + console.WorkMount
	if !in.Workspace.Writable {
		work += ":ro"
	}
	cache, err := r.cacheVolume(in.ID)
	if err != nil {
		return err
	}
	limits, err := r.limitArgs()
	if err != nil {
		return err
	}
	labels := append(writesLabel(in.Workspace), limits...)
	args := []string{"create", "-it", "--init", "--name", ContainerName(in.ID), "-w", console.WorkMount,
		"--label", dockerlabel.Class + "=console", "--label", dockerlabel.Inference + "=1", "--label", dockerlabel.Owner + "=" + in.ID, "--label", dockerlabel.Created + "=" + r.created(),
		"--label", dockerlabel.ConsoleDind + "=" + dindMode(in), "--label", dockerlabel.ConsoleModel + "=" + in.Model, "--label", dockerlabel.ConsoleMounts + "=" + r.mountsDigest(in),
		"--add-host", "host.docker.internal:host-gateway",
		"-v", work,
		"-v", in.Dir + ":" + console.Mount + ":ro",
		"-v", cache + ":/apptown-cache",
		"-e", "npm_config_cache=/apptown-cache/npm",
		"-e", "GOMODCACHE=/apptown-cache/gomod",
		"-e", "GOCACHE=/apptown-cache/gobuild",
		"-e", "UV_CACHE_DIR=/apptown-cache/uv",
		"-e", "HOME=" + console.Mount + "/pi-home",
		"-e", "TERM=xterm-256color",
		"-e", "PI_SKIP_VERSION_CHECK=1",
	}
	args = append(args, labels...)
	for _, name := range writableDirs() {
		dir := filepath.Join(in.Dir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		args = append(args, "-v", dir+":"+console.Mount+"/"+name)
	}
	for _, kv := range in.Env {
		args = append(args, "-e", kv)
	}
	for _, m := range r.mounts(in) {
		args = append(args, "-v", m)
	}
	if in.Docker {
		if err := r.ensureDind(in); err != nil {
			return fmt.Errorf("preparing console Docker: %w", err)
		}
		args = append(args, "--network", dindName(in.ID), "-e", "DOCKER_HOST=tcp://dind:2375",
			"-e", "DOCKER_CONFIG=/console/docker-client", "-e", "BUILDX_BUILDER=apptown")
	}
	// PID1 is sleep; pi runs under tmux via the launcher.
	// "--" ends Docker's options: nothing from the launch can become one.
	args = append(args, "--", r.image(in), "sleep", "infinity")
	if _, err := r.docker(args...); err != nil {
		return fmt.Errorf("creating container: %w", err)
	}
	return nil
}

// writesLabel marks a container that mounts the session's workspace
// writable (forge.writes=<host path>, docker.WritesLabel), so whoever next
// works in that folder can find it without knowing this session.
func writesLabel(w console.Workspace) []string {
	if !w.Writable {
		return nil
	}
	return []string{"--label", dockerlabel.Writes + "=" + w.Path}
}

// cacheClass labels a session's own cache volume.
const cacheClass = dockerlabel.Class + "=console-cache"

func cacheName(id string) string { return "apptown-cache-console-" + id }

// cacheVolume is the volume a session's container and daemon mount at
// /apptown-cache: apptown-cache, shared, or with CachePerSession the session's
// own, made labelled with the session if it is not there.
func (r *Runtime) cacheVolume(id string) (string, error) {
	if !r.cfg.CachePerSession {
		return "apptown-cache", nil
	}
	name := cacheName(id)
	if _, err := r.docker("volume", "create", "--label", cacheClass, "--label", dockerlabel.Console+"="+id, name); err != nil {
		return "", err
	}
	// Docker answers a volume already there as made, with its own labels.
	if out, err := r.docker("volume", "inspect", "--format", `{{index .Labels "`+dockerlabel.Console+`"}}`, name); err != nil {
		return "", err
	} else if strings.TrimSpace(string(out)) != id {
		return "", fmt.Errorf("the cache volume %s belongs to another owner", name)
	}
	return name, nil
}

// removeCache removes a session's own cache volume, and confirms it is
// gone. One with another owner is an error.
func (r *Runtime) removeCache(ctx context.Context, id string) error {
	if !r.cfg.CachePerSession {
		return nil
	}
	name := cacheName(id)
	out, err := r.command(ctx, "volume", "inspect", "--format", `{{index .Labels "`+dockerlabel.Console+`"}}`, name)
	switch {
	case err != nil && missingDockerObject(out):
		return nil
	case err != nil:
		return err
	case strings.TrimSpace(string(out)) != id:
		return fmt.Errorf("refusing to remove the cache volume %s with a different owner", name)
	}
	if out, err := r.command(ctx, "volume", "rm", name); err != nil && !missingDockerObject(out) {
		return err
	}
	if out, err := r.command(ctx, "volume", "inspect", name); err == nil {
		return fmt.Errorf("docker did not confirm %s removed", name)
	} else if !missingDockerObject(out) {
		return err
	}
	return nil
}

// limitArgs are the docker flags for Limits; none without them.
func (r *Runtime) limitArgs() ([]string, error) {
	l := r.cfg.Limits
	if l == nil {
		return nil, nil
	}
	if l.CPUs <= 0 || l.MemoryBytes <= 0 || l.PIDs <= 0 {
		return nil, fmt.Errorf("a chat needs CPU, memory and PID limits: zero is not unlimited (%+v)", *l)
	}
	return dind.LimitArgs(l.CPUs, l.MemoryBytes, l.PIDs), nil
}
