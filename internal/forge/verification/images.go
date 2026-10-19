package verification

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/strs"
)

// checkImages supplies container images and synthetic environments for checks.
// It may reuse the recorded artifact of a standing deployment; it never substitutes
// a moving tag for that digest and never boots or changes the deployment.
// imageForCommit resolves the app image to run an exec-image tool inside,
// content-addressed by the committed tree SHA so forge check, the land gate,
// and repeat invocations share ONE build instead of rebuilding each time
// (design/subsystems/conformance.md §13.6). `buildDir` is the staged committed tree (the
// build context root). `allowBuild` gates whether a cache miss may build: true
// in the loop (check) and at the gate, false in the read-only fleet sweep,
// which reuses a cached image if one exists but never triggers a build.
//
// status: "ok" (ref usable), "none" (app ships no image — n/a here),
// "deferred" (buildable, but allowBuild=false and nothing cached), "error".
//
// (Further step: the runtime path tags per-container-name, rebuilt each deploy;
// a shared content-addressed cache across conformance AND runtime would remove
// the last duplicate build. Out of scope here — this shares within the
// conformance paths, which is where the loop's repeat builds were.)
func (s *Scanners) imageForCommit(ctx context.Context, projectID ids.ID, buildDir, sha string, allowBuild bool) (ref, note, status string, err error) {
	c, cerr := docker.ReadWorkspaceContract(buildDir)
	if errors.Is(cerr, docker.ErrNoContract) {
		return "", "no app contract — nothing to run in an image", "none", nil
	}
	if cerr != nil {
		return "", "", "error", fmt.Errorf("reading app contract: %w", cerr)
	}
	// The image is named by its ID on the executor's machine: what a check
	// then runs and what its evidence records. A tag only finds it.
	switch {
	case c.PublicPart().Image != "":
		req := execution.ImageRequest{Pull: c.PublicPart().Image}
		if ri := s.integrations.RegistryFor(ctx, projectID); ri != nil {
			req.Registry = &execution.RegistryLogin{Host: ri.Host, Username: ri.Username, Token: ri.Token}
		}
		a := s.exec.Image(ctx, req)
		if a.Err != "" || a.ID == "" {
			return "", "", "error", fmt.Errorf("preparing %s: %s", c.PublicPart().Image, strs.FirstNonBlank(a.Err, "no image"))
		}
		return a.ID, "", "ok", nil
	case c.PublicPart().Build != "":
		// A tag cannot say "(none)": an unrecorded commit shares one tag.
		tag := "forge-conform:unknown"
		if strings.TrimSpace(sha) != "" {
			tag = "forge-conform:" + strs.ShortSHA(sha)
		}
		if a := s.exec.Image(ctx, execution.ImageRequest{Tag: tag}); a.Err == "" && a.ID != "" {
			// A cache hit cannot say who put the image there: a build for the
			// checks, or a release image recorded at this commit.
			return a.ID, cachedImageNote, "ok", nil // the shared build; no rebuild
		}
		// A release image is on this machine: reusing it applies only when
		// the checks run here, and never on a
		// forge with no local Docker, which holds no release image.
		if s.exec.Local && !s.docker.Absent() {
			if ref, note, ok := s.reuseReleaseImage(ctx, projectID, sha, tag); ok {
				// Named by its ID, not the tag the reuse moved.
				if a := s.exec.Image(ctx, execution.ImageRequest{Tag: ref}); a.Err == "" && a.ID != "" {
					return a.ID, note, "ok", nil
				}
			}
		}
		a := s.exec.Image(ctx, execution.ImageRequest{Tag: tag, Tree: buildDir, Context: filepath.Clean(c.PublicPart().Build), AllowBuild: allowBuild})
		switch {
		case a.NotBuilt:
			return "", "no image built for this commit yet — land it, run forge check, or start a review that builds", "deferred", nil
		case a.Err != "" || a.ID == "":
			return "", "", "error", errors.New(strs.FirstNonBlank(a.Err, "building the app image gave no image"))
		}
		return a.ID, "", "ok", nil
	}
	return "", staticPublicNote, "none", nil
}

const (
	// cachedImageNote: an image found in the commit's cache slot.
	cachedImageNote = "the image cached for this commit (built for the checks, or the release image recorded at this commit)"
	// staticPublicNote: the public part is a static site, which an image
	// check skips; where its files then run depends on the target.
	staticPublicNote = "the public part is a static site, which is skipped here: at release its files are packed into AppTown's file-server image, run as a container on container targets and published as files to a bucket"
)

// reuseReleaseImage reuses a standing deployment's release image for a
// conformance check when — and only when — the environment recorded that it
// was built from exactly this commit AND the image with that recorded digest
// still exists locally (artifact identity, not "same SHA"; design/
// conformance-surface.md §9 item 5). The image is tagged into the shared
// content-addressed slot so later checks hit the cache.
func (s *Scanners) reuseReleaseImage(ctx context.Context, projectID ids.ID, sha, tag string) (ref, note string, ok bool) {
	if strings.TrimSpace(sha) == "" {
		return "", "", false
	}
	placed, err := s.deployments.ForProject(ctx, projectID)
	if err != nil {
		return "", "", false
	}
	for _, e := range placed {
		if e.ReleaseSHA != strings.TrimSpace(sha) || e.ImageDigest == "" {
			continue
		}
		env, digest := e.Name, e.ImageDigest
		if _, ierr := s.docker.Run(ctx, "image", "inspect", digest); ierr != nil {
			continue // the recorded artifact is gone; never substitute another
		}
		if _, terr := s.docker.Run(ctx, "tag", digest, tag); terr != nil {
			return digest, fmt.Sprintf("reusing the %s release image %s", env, strs.Truncate(digest, 24)), true
		}
		return tag, fmt.Sprintf("reusing the %s release image %s", env, strs.Truncate(digest, 24)), true
	}
	return "", "", false
}

// checkEnvironment is the environment an exec-image check runs in: the
// shape of a deployed one, derived from what the app declares — never a
// hand-written guess (design/subsystems/conformance.md §13.5). Every secret in the
// declaration gets a value minted by the code that mints the real one (a
// short stand-in SECRET_KEY can fail a framework's own length rule that the
// real key meets); every
// declared need gets the connection env its emulator injects, pointed at a
// host that does not answer (the check runs with no network); FORGE_ORIGINS
// is set as the forge sets it in every environment. Nothing else — what the
// forge does not set in a deployment it does not set here either.
func (s *Scanners) checkEnvironment(ctx context.Context, buildDir string) []string {
	const host = "check.forge.local"
	env := map[string]string{"FORGE_ORIGINS": "https://" + host}
	if spec, present, err := secret.ReadSpec(buildDir, ""); err == nil && present {
		profile := "default"
		if _, ok := spec.Profiles[profile]; !ok {
			if names := spec.ProfileNames(); len(names) > 0 {
				profile = names[0]
			}
		}
		for _, s := range spec.SecretsIn(profile) {
			v := s.Default
			if v == "" {
				v, _ = secret.GenerateValue()
			}
			env[s.Name] = v
		}
	}
	if c, err := docker.ReadWorkspaceContract(buildDir); err == nil {
		for k, v := range s.dependencies.ResolveLocal(ctx, c.Needs, c.NeedParams, host).Env {
			env[k] = v
		}
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}
