package docker

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// Build runs `docker build args...` on dk's daemon, with that daemon's own
// builder named explicitly. Every build of a project's source AppTown
// makes on a daemon goes through it, so the source and its build go to the
// daemon AppTown selected and nowhere else.
//
// Naming the builder is the one rule: plain `docker build` otherwise
// honours an ambient selection, a builder alias (`docker buildx install`)
// with a saved remote builder, or BUILDX_BUILDER. The builder is the daemon's own: "default" for a
// daemon chosen by --host, otherwise the one named after the Docker context
// in use. It must use the docker driver (the daemon's own BuildKit), or the
// build is refused before anything is sent. Without buildx the CLI builds
// with the daemon's legacy builder, which nothing can redirect.
func (dk Scope) Build(ctx context.Context, args ...string) ([]byte, error) {
	builder, err := dk.ownBuilder(ctx)
	if err != nil {
		return nil, err
	}
	if builder != "" {
		args = append([]string{"--builder", builder}, args...)
	}
	return dk.Run(ctx, append([]string{"build"}, args...)...)
}

func (dk Scope) ownBuilder(ctx context.Context) (string, error) {
	if out, err := dk.Run(ctx, "buildx", "version"); err != nil || !strings.Contains(string(out), "buildx") {
		return "", nil // no buildx: the daemon's own legacy builder
	}
	name := "default"
	if dk.Host == "" {
		out, err := dk.Run(ctx, "context", "show")
		if err != nil {
			return "", fmt.Errorf("asking Docker which context it uses: %w: %s", err, strings.TrimSpace(string(out)))
		}
		if ctx := strings.TrimSpace(string(out)); ctx != "" {
			name = ctx
		}
	}
	out, err := dk.Run(ctx, "buildx", "inspect", name)
	if err != nil {
		return "", fmt.Errorf("asking Docker for the daemon's own builder %q: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	driver := ""
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Driver:"); ok {
			driver = strings.TrimSpace(v)
			break
		}
	}
	if driver != "docker" {
		return "", fmt.Errorf("the builder %q is not the selected daemon's own (its driver is %q, not docker): the build is not sent to it", name, driver)
	}
	return name, nil
}

// buildPreviewImage builds a preview's image as tag (args are the rest of
// the build's arguments, its context last). The image carries its build
// cache (inline), and the image tag names on this daemon, when there is
// one, is the build's cache source: a preview stopped and opened again
// builds from it though the daemon's own build cache was trimmed since.
// BuildKit decides what matches. Without the tag there is no cache source
// to name: BuildKit would ask a registry for a repository of that name.
// The image the tag named before goes once the tag names another
// (replacedImage).
func buildPreviewImage(ctx context.Context, dk Scope, tag string, args ...string) ([]byte, error) {
	before := dk.imageID(ctx, tag)
	flags := []string{"-t", tag, "--build-arg", "BUILDKIT_INLINE_CACHE=1"}
	if before != "" {
		flags = append(flags, "--cache-from", tag)
	}
	out, err := dk.Build(ctx, append(flags, args...)...)
	if err == nil {
		removeReplacedImage(ctx, dk, tag, before)
	}
	return out, err
}

// imageID is the ID of the image ref names on dk, "" for none (or one that
// cannot be read).
func (dk Scope) imageID(ctx context.Context, ref string) string {
	out, err := dk.Run(ctx, "image", "inspect", "--format", "{{.Id}}", ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// removeReplacedImage removes the image tag named before a build, by its
// ID, once nothing names it: the tag moved to another, and untagged no
// listing by name finds it again. An image a tag or a digest still names
// (this tag, when the build made the same image; another part's, another
// preview's, a release's) is not this build's to remove, nor one whose
// references cannot be read. One a container still uses stays, and is
// dangling once it is free; a failure is logged.
func removeReplacedImage(ctx context.Context, dk Scope, tag, before string) {
	if before == "" {
		return
	}
	refs, err := dk.Run(ctx, "image", "inspect", "--format", "{{len .RepoTags}} {{len .RepoDigests}}", before)
	if err != nil || strings.TrimSpace(string(refs)) != "0 0" {
		return
	}
	if _, err := dk.RemoveImageUnlessUsed(ctx, before); err != nil {
		log.Printf("removing the image %s no longer named: %v", tag, err)
	}
}
