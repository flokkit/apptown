package maintenance

import (
	"context"
	"log"
	"strings"

	"github.com/flokkit/apptown/internal/containerresult"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/strs"

	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// ---- the cache volume ------------------------------------------------------
//
// Each workspace gets its own toolchain install under
// /apptown-cache/mise/ws/<workspace> (registries.go: isolation, not sharing).
// Nothing removed them when the workspace's change closed, and at a few
// hundred MB each they fill a disk. Same owner rule: a closed
// change's or finished run's toolchain goes; a live one stays; anything not
// named for a change or a run is left alone. The build cache is kept under a
// budget and the dangling images AppTown built are pruned in the same sweep.

// reapCacheDecision says whether one per-workspace cache directory is
// reapable. Pure, so it is tested without a daemon.
func reapCacheDecision(entry string, runStatus func(ids.ID) workflow.RunState, changeState func(ids.ID) workflow.ChangeState) (bool, string) {
	switch {
	case strings.HasPrefix(entry, "change-"):
		id := parseTrailingID(entry)
		if id.IsZero() {
			return false, ""
		}
		st := changeState(id)
		if !st.Closed() {
			return false, ""
		}
		return true, "its change " + st.String()
	case strings.HasPrefix(entry, "run-"):
		id := parseTrailingID(entry)
		if id.IsZero() {
			return false, ""
		}
		st := runStatus(id)
		if st == 0 || st == workflow.RunRunning {
			return false, ""
		}
		return true, "its run " + st.String()
	}
	return false, ""
}

// reapCache sweeps each project's cache volume (and the shared apptown-cache
// one older steps wrote) once, through a throwaway container: the volume is
// the daemon's; apptownd never assumes its host path. A chat's own cache
// (apptown-cache-console-*) goes with its chat and is not swept here.
func (r *Reaper) Cache(ctx context.Context, logOnly bool) []string {
	if r.docker.Absent() {
		return nil // no local Docker, nothing of it to reap
	}
	pre := string(docker.Current) + "cache"
	volumes := []string{pre}
	for _, v := range r.docker.Lines(ctx, "volume", "ls", "--format", "{{.Name}}", "--filter", "name="+pre+"-") {
		if strings.HasPrefix(v, pre+"-") && !strings.HasPrefix(v, pre+"-console-") {
			volumes = append(volumes, v)
		}
	}
	projectCaches, err := agentenv.ProjectCaches(ctx, r.docker)
	if err != nil {
		log.Printf("reaper: project package caches not read: %v", err)
		return nil
	}
	volumes = append(volumes, projectCaches...)
	runStatus := func(id ids.ID) workflow.RunState {
		r, err := workflow.ReadRunIn(ctx, r.db, id)
		if err != nil || r == nil {
			return 0
		}
		return r.Status
	}
	changeState := func(id ids.ID) workflow.ChangeState {
		c, err := workflow.ReadChangeIn(ctx, r.db, id)
		if err != nil || c == nil {
			return 0
		}
		return c.State
	}
	var reaped []string
	var sh string
	if r.tools != nil {
		sh = r.tools.Current().Images.Sh
	}
	if sh == "" {
		log.Printf("reaper: toolchain caches not read: no shell image in the tool versions")
		return nil
	}
	for _, volume := range volumes {
		if ctx.Err() != nil {
			return reaped // stopped: the rest wait for the next sweep
		}
		// The listing decides what is removed, so it is read from a file in
		// the container (containerresult.Run), never its output: a volume
		// whose listing did not come back is left as it is.
		out, err := r.docker.Result(ctx, containerresult.Spec{Image: sh, Options: []string{"-v", volume + ":/apptown-cache"}, Script: "ls /apptown-cache/mise/ws"})
		if err != nil {
			continue
		}
		for _, entry := range strings.Fields(string(out)) {
			ok, why := reapCacheDecision(entry, runStatus, changeState)
			if !ok {
				continue
			}
			reaped = append(reaped, volume+"/"+entry)
			if logOnly {
				log.Printf("reaper: WOULD remove toolchain cache %s in %s (%s)", entry, volume, why)
				continue
			}
			log.Printf("reaper: removing toolchain cache %s in %s (%s)", entry, volume, why)
			if out, err := r.docker.Run(ctx, "run", "--rm", "--entrypoint", "", "-v", volume+":/apptown-cache", sh, "rm", "-rf", "/apptown-cache/mise/ws/"+entry); err != nil {
				log.Printf("reaper: toolchain cache %s in %s not removed: %s", entry, volume, strs.Truncate(string(out), 120))
			}
		}
	}
	if !logOnly {
		_ = docker.TrimBuildCache(ctx, r.docker)
		_ = docker.PruneBuiltImages(ctx, r.docker)
	}
	return reaped
}
