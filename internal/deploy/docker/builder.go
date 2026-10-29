package docker

import (
	"context"
	"log"
	"strings"

	"github.com/flokkit/apptown/internal/dockerbuild"
)

// Build runs `docker build args...` on dk's daemon by the one build rule
// (internal/dockerbuild): that daemon's own builder named explicitly, the
// extra certificates as the build's secret, AppTown's label. Every build of
// a project's source AppTown makes on a daemon goes through it, so the
// source and its build go to the daemon AppTown selected and nowhere else.
func (dk Scope) Build(ctx context.Context, args ...string) ([]byte, error) {
	return dk.BuildCommand(ctx, Command{Args: args})
}

// BuildCommand is Build with the command's other settings (where its output
// goes): req.Args are the build's own arguments, its context last.
func (dk Scope) BuildCommand(ctx context.Context, req Command) ([]byte, error) {
	full, err := dockerbuild.Daemon{Run: dk.Run, HostChosen: dk.Host != "", Local: !dk.Remote()}.Args(ctx, req.Args...)
	if err != nil {
		return nil, err
	}
	req.Args = full
	return dk.Command(ctx, req)
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
