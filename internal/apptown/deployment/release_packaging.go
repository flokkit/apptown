package deployment

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// releaseBuildFiles packages the declared source for a registry-backed target.
// Static files use the same pinned server and self-describing image as a
// local static boot (docker.StaticSiteDockerfile).
// Existing Dockerfiles outside an explicit build context have no influence.
func releaseBuildFiles(source string, c *appcontract.Contract) (dir, dockerfile string, cleanup func(), err error) {
	cleanup = func() {}
	path := c.PublicPart().Build
	if c.PublicPart().Static != "" {
		path = c.PublicPart().Static
	}
	if sb := c.PublicPart().StaticBuild; sb != nil {
		// The build context: its Dockerfile is the site's recipe. The
		// build itself runs only when the release is built
		// (docker.ExportStaticBuild), never in this check.
		path = sb.Context
		if _, err := docker.SelectedPath(source, filepath.Join(path, "Dockerfile")); err != nil {
			return "", "", cleanup, fmt.Errorf("the static site's build %q needs its Dockerfile: %w", path, err)
		}
	}
	if path == "" {
		return "", "", cleanup, fmt.Errorf("release requires an explicit build or static source")
	}
	dir, err = docker.SelectedPath(source, path)
	if err != nil {
		return "", "", cleanup, err
	}
	if !c.PublicPart().IsStatic() {
		file, err := docker.SelectedPath(source, filepath.Join(path, "Dockerfile"))
		if err != nil {
			return "", "", cleanup, fmt.Errorf("declared build context %q needs its Dockerfile: %w", path, err)
		}
		return dir, file, cleanup, nil
	}
	f, err := os.CreateTemp("", "apptown-release-static-Dockerfile-")
	if err != nil {
		return "", "", cleanup, err
	}
	cleanup = func() { _ = os.Remove(f.Name()) }
	_, writeErr := f.WriteString(docker.StaticSiteDockerfile(docker.StaticServerImage, c.PublicPart().Port))
	if err := errors.Join(writeErr, f.Close()); err != nil {
		cleanup()
		return "", "", func() {}, err
	}
	return dir, f.Name(), cleanup, nil
}
