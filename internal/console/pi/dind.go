package pi

import (
	"context"
	"encoding/json"
	"flokkit.io/forge/internal/dockerlabel"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/dind"
)

// DindConfig is the private Docker daemon a change console gets: the agent
// builds and runs its own containers there, never on AppTown's daemon.
// The daemon and its builder use this machine's resolvers and search
// domains, and the launch's mirror (dind.DaemonOptions).
type DindConfig struct {
	Image string // the docker:dind image
	// Mark runs before any privileged container is created or started: a
	// worker records that it runs privileged agents (worker.PrivilegedMark).
	// Its error creates nothing. Nil marks nothing.
	Mark func() error
}

func dindName(id string) string { return "forge-dind-console-" + id }

// ensureDind is called under the session's attachMu (or before initial
// publication). It never replaces a surviving daemon: its nested images and
// containers are session state. Missing resources are reconstructed.
func (r *Runtime) ensureDind(in console.Launch) error {
	if r.cfg.Dind.Mark != nil {
		if err := r.cfg.Dind.Mark(); err != nil {
			return fmt.Errorf("private Docker refused: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(r.ctx, 60*time.Second)
	defer cancel()
	name := dindName(in.ID)
	clientDir := filepath.Join(in.Dir, "docker-client")
	configDir := filepath.Join(in.Dir, "docker-runtime")
	for _, dir := range []string{clientDir, configDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	if _, err := os.Stat(in.Workspace.Path); err != nil {
		return fmt.Errorf("private Docker workspace: %w", err)
	}
	out, err := r.command(ctx, "network", "inspect", "--format", `{{index .Labels "`+dockerlabel.Console+`"}}`, name)
	if err != nil {
		if !missingDockerObject(out) {
			return err
		}
		if _, err := r.command(ctx, "network", "create", "--label", dockerlabel.Console+"="+in.ID, name); err != nil {
			return err
		}
	} else if strings.TrimSpace(string(out)) != in.ID {
		return fmt.Errorf("private Docker network %s belongs to another owner", name)
	}
	const inspect = `{"running":{{.State.Running}},"owner":{{json (index .Config.Labels "` + dockerlabel.Console + `")}},"mounts":{{json .Mounts}},"networks":{{json .NetworkSettings.Networks}}}`
	// The network has the same name: an untyped inspect would return the
	// network when the container is absent, disguising a recoverable miss.
	out, err = r.command(ctx, "inspect", "--type", "container", "--format", inspect, name)
	if err != nil {
		if !missingDockerObject(out) {
			return err
		}
		cache, err := r.cacheVolume(in.ID)
		if err != nil {
			return err
		}
		limits, err := r.limitArgs()
		if err != nil {
			return err
		}
		daemon := dind.Daemon{Name: name, Network: name, Image: r.cfg.Dind.Image, OwnerLabel: dockerlabel.Console, Owner: in.ID,
			Options: dind.DaemonOptions(in.Mirror),
			Args: append(append(writesLabel(in.Workspace), limits...), "--label", dockerlabel.Class+"=console-dind",
				"--label", dockerlabel.Owner+"="+in.ID, "--label", dockerlabel.Created+"="+r.created(),
				"-v", in.Workspace.Path+":/work"+readOnlyMount(in.Workspace), "-v", cache+":/apptown-cache",
				"-v", clientDir+":/docker-client", "-v", configDir+":/forge-dind:ro")}
		if err := daemon.Create(ctx, r.command); err != nil {
			return err
		}
		logf("%s: created private Docker daemon", in.ID)
	} else {
		var st struct {
			Running bool
			Owner   string
			Mounts  []struct {
				Type, Source, Destination string
				RW                        bool
			}
			Networks map[string]json.RawMessage
		}
		if err := json.Unmarshal(out, &st); err != nil {
			return fmt.Errorf("reading private Docker state: %w", err)
		}
		workspaceOK := false
		for _, mount := range st.Mounts {
			if mount.Type == "bind" && mount.Destination == "/work" && mount.RW && sameWorkspaceDirectory(mount.Source, in.Workspace.Path) {
				workspaceOK = true
			}
		}
		if st.Owner != in.ID || !workspaceOK {
			return fmt.Errorf("private Docker %s has a different owner or workspace", name)
		}
		if _, joined := st.Networks[name]; !joined {
			if _, err := r.command(ctx, "network", "connect", "--alias", "dind", name, name); err != nil {
				return err
			}
		}
		if !st.Running {
			if _, err := r.command(ctx, "start", name); err != nil {
				return err
			}
			logf("%s: restarted private Docker daemon", in.ID)
		}
	}
	if err := dind.Ready(ctx, r.command, name); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(configDir, "buildkitd.toml"), []byte(dind.BuildkitConfig(in.Mirror)), 0o644); err != nil {
		return err
	}
	// Builder state is shared with the console's Docker client, outside the
	// worktree.
	return dind.Builder(ctx, r.command, name, "/docker-client", "/forge-dind/buildkitd.toml")
}

// Docker Desktop may rewrite both Mounts.Source and HostConfig.Binds to VM
// paths, including resolving macOS's /var symlink to /private/var. Compare the
// actual host directory; a string-prefix match would admit another workspace.
func sameWorkspaceDirectory(source, workspace string) bool {
	if runtime.GOOS == "darwin" && strings.HasPrefix(source, "/host_mnt/") {
		source = strings.TrimPrefix(source, "/host_mnt")
	}
	if !filepath.IsAbs(source) {
		return false
	}
	want, err := os.Stat(workspace)
	if err != nil {
		return false
	}
	got, err := os.Stat(source)
	return err == nil && got.IsDir() && want.IsDir() && os.SameFile(got, want)
}

// connectClient joins the console container to its daemon's network if a
// recreation left it out.
func (r *Runtime) connectClient(id string) error {
	ctx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
	defer cancel()
	out, err := r.command(ctx, "inspect", "--type", "container", "--format", `{{json .NetworkSettings.Networks}}`, ContainerName(id))
	if err != nil {
		return err
	}
	var networks map[string]json.RawMessage
	if err := json.Unmarshal(out, &networks); err != nil {
		return err
	}
	if _, joined := networks[dindName(id)]; joined {
		return nil
	}
	_, err = r.command(ctx, "network", "connect", dindName(id), ContainerName(id))
	return err
}

// sidecar reads whether the session's private daemon exists and runs.
func (r *Runtime) sidecar(ctx context.Context, id string) (exists, running bool, err error) {
	return dind.State(ctx, r.command, dindName(id), dockerlabel.Console, id)
}

// ownedNetwork protects unrelated networks even if a name was reused.
func (r *Runtime) ownedNetwork(ctx context.Context, id string) (bool, error) {
	out, err := r.command(ctx, "network", "inspect", "--format", `{{index .Labels "`+dockerlabel.Console+`"}}`, dindName(id))
	if err != nil && missingDockerObject(out) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(string(out)) != id {
		return false, fmt.Errorf("refusing to change private Docker resource %s with a different owner", dindName(id))
	}
	return true, nil
}

// disposeDind stops or removes the session's private daemon (and, when
// removing, its network and its own cache volume). A failure is returned: a
// daemon that may still be running, or a network or volume still present,
// is not a finished stop or removal.
func (r *Runtime) disposeDind(id string, remove bool) error {
	if err := r.disposeDindWithin(r.ctx, id, remove); err != nil {
		return fmt.Errorf("private Docker cleanup: %w", err)
	}
	return nil
}

func (r *Runtime) disposeDindWithin(parent context.Context, id string, remove bool) error {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	if !remove {
		return dind.Stop(ctx, r.command, dindName(id), dockerlabel.Console, id)
	}
	if err := dind.Remove(ctx, r.command, dindName(id), dockerlabel.Console, id); err != nil {
		return err
	}
	owned, err := r.ownedNetwork(ctx, id)
	if err != nil {
		return err
	}
	if owned {
		if _, err := r.command(ctx, "network", "rm", dindName(id)); err != nil {
			return err
		}
	}
	return r.removeCache(ctx, id)
}

// Reap removes private daemons, networks and console containers whose
// session no longer exists. Ownership is recorded on disk before
// provisioning, so unfinished creation is recoverable, never an orphan.
// Read failures other than absence preserve the resources for a later try.
func (r *Runtime) Reap(_ context.Context, alive map[string]bool) error {
	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
	defer cancel()
	owners := map[string]bool{}
	for _, args := range [][]string{
		{"ps", "-a", "--filter", "label=" + dockerlabel.Class + "=console-dind", "--format", `{{.Label "` + dockerlabel.Console + `"}}`},
		{"network", "ls", "--filter", "label=" + dockerlabel.Console, "--format", `{{.Label "` + dockerlabel.Console + `"}}`},
		{"volume", "ls", "--filter", "label=" + cacheClass, "--format", `{{.Label "` + dockerlabel.Console + `"}}`},
	} {
		out, err := r.command(ctx, args...)
		if err != nil {
			logf("Docker orphan discovery: %v", err)
			return nil
		}
		for _, id := range strings.Fields(string(out)) {
			owners[id] = true
		}
	}
	for id := range owners {
		if ctx.Err() != nil {
			logf("Docker orphan cleanup: %v", ctx.Err())
			return nil
		}
		if !validID(id) || alive[id] {
			continue
		}
		// A crash during creation or end may also leave the client connected.
		out, err := r.command(ctx, "inspect", "--type", "container", "--format", `{{index .Config.Labels "`+dockerlabel.Owner+`"}}`, ContainerName(id))
		if err == nil && strings.TrimSpace(string(out)) == id {
			if _, err := r.command(ctx, "rm", "-f", ContainerName(id)); err != nil {
				logf("%s orphan cleanup: %v", id, err)
				continue
			}
		} else if err != nil && !missingDockerObject(out) {
			continue
		}
		logf("%s: removing orphaned private Docker", id)
		if err := r.disposeDindWithin(ctx, id, true); err != nil {
			logf("%s orphan cleanup: %v", id, err)
		}
	}
	return nil
}

func validID(id string) bool {
	if id == "" {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// readOnlyMount is the mount suffix for a workspace the chat may not write:
// its Docker sidecar honours it as its agent does.
func readOnlyMount(w console.Workspace) string {
	if w.Writable {
		return ""
	}
	return ":ro"
}
