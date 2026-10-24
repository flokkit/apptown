package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/strs"
)

// PrepareImages prepares each selected app and init image once, on the
// selected daemon. Standing containers run image IDs, never a tag another
// generation can replace. Static files travel in an image too; no runtime
// mount retains the staging directory.
func (h *Host) PrepareImages(ctx context.Context, t BootRequest, c *appcontract.Plan) (*appcontract.Plan, error) {
	cc := *c
	cc.Parts = append([]appcontract.Part(nil), c.Parts...)
	cc.Init = append([]appcontract.InitContainer(nil), c.Init...)
	built := map[string]string{}
	prepare := func(name, build, static string, sb *appcontract.StaticBuild, image string, port int, recorded string) (string, error) {
		if t.Recorded != nil {
			t.logf("Preparing recorded image %s on the Docker host", name)
			id, err := t.Recorded.onDaemon(ctx, t.Docker, name, recorded)
			if err == nil && IsDigestReference(recorded) {
				err = t.own.created(deploy.Owned{Kind: "image", ID: recorded})
			}
			return id, err
		}
		key := build + "\x00" + static + "\x00" + image
		if sb != nil {
			key += "\x00" + sb.Context + "\x00" + sb.Target + "\x00" + sb.Output
		}
		if static != "" || sb != nil {
			key += fmt.Sprintf("\x00%d", port) // the port is in the image's command
		}
		if id := built[key]; id != "" {
			return id, nil
		}
		t.logf("Preparing image %s on the Docker host", name)
		if sb != nil {
			root, err := SelectedPath(t.Workspace, sb.Context)
			if err != nil {
				return "", err
			}
			image = Current.BuildTag(fmt.Sprintf("%s-g%d-%s", t.Name, t.Identity.Generation, name))
			if err := PackStaticBuild(ctx, t.Docker, root, *sb, h.staticServer(), port, image); err != nil {
				return "", err
			}
			if err := t.own.created(deploy.Owned{Kind: "image", ID: image}); err != nil {
				return "", err
			}
		} else if build != "" || static != "" {
			path := build
			if static != "" {
				path = static
			}
			root, err := SelectedPath(t.Workspace, path)
			if err != nil {
				return "", err
			}
			tag := Current.BuildTag(fmt.Sprintf("%s-g%d-%s", t.Name, t.Identity.Generation, name))
			args := []string{"-t", tag}
			if static != "" {
				dockerfile, remove, err := staticDockerfile(h.staticServer(), port)
				if err != nil {
					return "", err
				}
				defer remove()
				args = append(args, "-f", dockerfile)
			}
			args = append(args, root)
			if out, err := t.Docker.Build(ctx, args...); err != nil {
				return "", BuildFailure("building selected Docker source", out, err)
			}
			if err := t.own.created(deploy.Owned{Kind: "image", ID: tag}); err != nil {
				return "", err
			}
			image = tag
		} else if !IsLocalBuildTag(image) && !strings.HasPrefix(image, "sha256:") {
			if err := h.login(ctx, t.Docker, image); err != nil {
				return "", fmt.Errorf("pulling selected image %s: %w", image, err)
			}
			if out, err := t.Docker.Run(ctx, "pull", image); err != nil {
				return "", fmt.Errorf("pulling selected image %s: %s", image, strs.Truncate(string(out), 300))
			}
		}
		out, err := t.Docker.Run(ctx, "image", "inspect", "--format", "{{.Id}}", image)
		id := strings.TrimSpace(string(out))
		if err != nil || !strings.HasPrefix(id, "sha256:") {
			return "", fmt.Errorf("selected image %s has no verified image ID: %w", image, err)
		}
		built[key] = id
		return id, nil
	}
	for i := range cc.Parts {
		p := &cc.Parts[i]
		id, err := prepare(p.Name, p.Build, p.Static, p.StaticBuild, p.Image, p.Port, t.Recorded.part(p.Name))
		if err != nil {
			return nil, err
		}
		// A static part's image carries its own command
		// (StaticSiteDockerfile); it is still a static site.
		p.ServesStatic = p.IsStatic()
		p.Image, p.Build, p.Static, p.StaticBuild = id, "", "", nil
	}
	for i := range cc.Init {
		it := &cc.Init[i]
		id, err := prepare("init-"+it.Name, it.Build, "", nil, it.Image, 0, t.Recorded.init(it.Name))
		if err != nil {
			return nil, err
		}
		it.Image, it.Build = id, ""
	}
	if cc.PublicPart().Image == "" {
		return nil, errors.New("selected Docker contract has no public image")
	}
	return &cc, nil
}

// RecordedImages are a release's images by part and by init step: image
// IDs as recorded on the daemon that built them, or images by digest in the
// release's registry (<repository>@sha256:<hex>), which any daemon pulls.
type RecordedImages struct {
	Parts, Init map[string]string
	// Login is the registry credential images by digest are pulled with,
	// written into a Docker configuration of the pull's own under Scratch
	// (WithBootConfig), never logged in where another command could find
	// it. nil pulls anonymously.
	Login   *RegistryCredential
	Scratch string
}

// ErrRecordedImageMissing is a recorded image the daemon no longer has, or
// a digest its registry confirms it does not have.
var ErrRecordedImageMissing = errors.New("a recorded image is not on the daemon")

var digestReference = regexp.MustCompile(`^[^@\s]+@sha256:[a-f0-9]{64}$`)

// IsDigestReference says whether ref names an image by digest in a
// registry: <repository>@sha256:<hex>.
func IsDigestReference(ref string) bool { return digestReference.MatchString(ref) }

// Present verifies every recorded image is on the daemon under its ID.
func (r *RecordedImages) Present(ctx context.Context, dk Scope) error {
	for name, id := range r.Parts {
		if _, err := onDaemon(ctx, dk, name, id); err != nil {
			return err
		}
	}
	for name, id := range r.Init {
		if _, err := onDaemon(ctx, dk, "init-"+name, id); err != nil {
			return err
		}
	}
	return nil
}

func (r *RecordedImages) part(name string) string {
	if r == nil {
		return ""
	}
	return r.Parts[name]
}

func (r *RecordedImages) init(name string) string {
	if r == nil {
		return ""
	}
	return r.Init[name]
}

// onDaemon is the image a recorded reference runs as on the daemon: an
// image by digest pulled there (pull), or an image ID verified present.
func (r *RecordedImages) onDaemon(ctx context.Context, dk Scope, name, ref string) (string, error) {
	if IsDigestReference(ref) {
		return r.pull(ctx, dk, name, ref)
	}
	return onDaemon(ctx, dk, name, ref)
}

// pull pulls an image by digest onto the daemon and answers the ID the
// container runs, once the daemon holds it under that digest (its
// RepoDigests). Nothing is built. A digest the registry answers it does not
// have (manifest unknown) is ErrRecordedImageMissing: the release is gone
// from its registry. A registry that refuses the credential or cannot be
// reached, and a daemon that cannot be asked, are errors, never "missing".
func (r *RecordedImages) pull(ctx context.Context, dk Scope, name, ref string) (string, error) {
	pulling, done, err := dk.WithBootConfig(ctx, r.Scratch, r.Login)
	if err != nil {
		return "", fmt.Errorf("a Docker configuration for pulling %s (%s): %w", ref, name, err)
	}
	out, err := pulling.Run(ctx, "pull", ref)
	if doneErr := done(); doneErr != nil {
		log.Printf("removing a pull's Docker configuration: %v", doneErr)
	}
	if err != nil {
		answer := strs.Truncate(strings.TrimSpace(string(out)), 300)
		if strings.Contains(strings.ToLower(answer), "manifest unknown") {
			return "", fmt.Errorf("%w: the registry no longer has %s (%s); land the change again to release it: %s", ErrRecordedImageMissing, ref, name, answer)
		}
		return "", fmt.Errorf("pulling %s (%s): %v: %s", ref, name, err, answer)
	}
	out, err = dk.Run(ctx, "image", "inspect", "--format", "{{.Id}} {{json .RepoDigests}}", ref)
	if err != nil {
		return "", fmt.Errorf("asking the daemon for the pulled %s (%s): %v: %s", ref, name, err, strs.Truncate(strings.TrimSpace(string(out)), 300))
	}
	id, raw, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	var held []string
	_ = json.Unmarshal([]byte(raw), &held)
	digest := ref[strings.LastIndex(ref, "@"):]
	if !strings.HasPrefix(id, "sha256:") || !slices.ContainsFunc(held, func(d string) bool { return strings.HasSuffix(d, digest) }) {
		return "", fmt.Errorf("the daemon does not hold %s (%s) under its digest: it answers %s", ref, name, strs.Truncate(strings.TrimSpace(string(out)), 300))
	}
	return id, nil
}

// onDaemon is a recorded image ID, verified present on the daemon. A daemon
// that cannot be asked is an error, never "missing".
func onDaemon(ctx context.Context, dk Scope, name, id string) (string, error) {
	if !strings.HasPrefix(id, "sha256:") {
		return "", fmt.Errorf("%w: the release records no image ID for %s", ErrRecordedImageMissing, name)
	}
	out, err := dk.Run(ctx, "image", "inspect", "--format", "{{.Id}}", id)
	got := strings.TrimSpace(string(out))
	if err != nil && strings.Contains(strings.ToLower(got), "no such image") {
		return "", fmt.Errorf("%w: %s (%s)", ErrRecordedImageMissing, id, name)
	}
	if err != nil {
		return "", fmt.Errorf("asking the daemon for image %s (%s): %v: %s", id, name, err, strs.Truncate(got, 300))
	}
	if got != id {
		return "", fmt.Errorf("%w: %s (%s) answers as %q", ErrRecordedImageMissing, id, name, strs.Truncate(got, 100))
	}
	return id, nil
}
