// Package dockerbuild is how AppTown builds an image on a Docker daemon:
// with that daemon's own builder named explicitly, the extra certificates
// as the build's secret and the label that says AppTown made the image.
// Every build on a daemon goes through it (deployments, previews, check
// images, the agent image, on AppTown's machine and on a worker), so the
// source and its build go to the daemon chosen and nowhere else.
//
// Naming the builder is the one rule: plain `docker build` otherwise
// honours an ambient selection, a builder alias (`docker buildx install`)
// with a saved remote builder, or BUILDX_BUILDER. The builder is the
// daemon's own: "default" for a daemon chosen by --host, otherwise the one
// named after the Docker context in use. It must use the docker driver (the
// daemon's own BuildKit), or the build is refused before anything is sent.
// Buildx is required beside the docker CLI: without it the build is
// refused, saying so, rather than falling back to the daemon's legacy
// builder.
package dockerbuild

import (
	"context"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/trust"
)

// run runs one docker command (the arguments after the binary) on the
// daemon the build is for, and answers with its output.
type run func(ctx context.Context, args ...string) ([]byte, error)

// Daemon is the daemon a build goes to, as the builder rule needs it.
type Daemon struct {
	Run run
	// HostChosen says the daemon was chosen with --host (another
	// machine's): its builder is "default", not the context's.
	HostChosen bool
	// Local says the daemon is on this machine, so the extra certificates
	// are this machine's (trust.BuildArgs).
	Local bool
}

// Args is the whole `docker build` command line for args (the build's own
// arguments, its context last), the builder chosen by the rule above.
func (d Daemon) Args(ctx context.Context, args ...string) ([]string, error) {
	builder, err := d.ownBuilder(ctx)
	if err != nil {
		return nil, err
	}
	out := []string{"build"}
	// The extra certificates, as the secret a Dockerfile's RUN lines may
	// mount (internal/trust).
	out = append(out, trust.BuildArgs(d.Local)...)
	// Who made the image, for the maintenance sweep.
	out = append(out, dockerlabel.BuiltArgs()...)
	out = append(out, "--builder", builder)
	return append(out, args...), nil
}

// Build runs the build Args makes.
func (d Daemon) Build(ctx context.Context, args ...string) ([]byte, error) {
	full, err := d.Args(ctx, args...)
	if err != nil {
		return nil, err
	}
	return d.Run(ctx, full...)
}

func (d Daemon) ownBuilder(ctx context.Context) (string, error) {
	if out, err := d.Run(ctx, "buildx", "version"); err != nil || !strings.Contains(string(out), "buildx") {
		said := strings.TrimSpace(string(out))
		if err != nil {
			said = strings.TrimSpace(err.Error() + " " + said)
		}
		return "", fmt.Errorf("building images needs Docker's buildx plugin, and AppTown's Docker client could not run `docker buildx version` (%q): install the buildx plugin beside the docker CLI AppTown runs (the docker-buildx-plugin package)", said)
	}
	name := "default"
	if !d.HostChosen {
		out, err := d.Run(ctx, "context", "show")
		if err != nil {
			return "", fmt.Errorf("asking Docker which context it uses: %w: %s", err, strings.TrimSpace(string(out)))
		}
		if ctx := strings.TrimSpace(string(out)); ctx != "" {
			name = ctx
		}
	}
	out, err := d.Run(ctx, "buildx", "inspect", name)
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
