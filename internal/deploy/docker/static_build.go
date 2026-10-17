package docker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"flokkit.io/forge/internal/deploy/appcontract"
)

// PackStaticBuild builds a declared static site on dk and packs its output
// into the file server's image, tagged tag (design/subsystems/deploy-targets.md,
// "Static sites and readiness"):
//
//  1. export: the Dockerfile in contextDir is built to its target stage and
//     that stage's files are written to a folder (docker build --output);
//     nothing of it runs afterwards;
//  2. confine: the output folder is checked before anything reads it
//     (ConfineStaticExport);
//  3. pack: the folder becomes the self-serving static image
//     (StaticSiteDockerfile), as a committed static folder does.
//
// Both builds are plain `docker build` on dk, so they run on the daemon the
// forge selected and its own builder (CLI.Run drops any inherited
// BUILDX_BUILDER), never on a Buildx builder chosen elsewhere.
func PackStaticBuild(ctx context.Context, dk Scope, contextDir string, sb appcontract.StaticBuild, server string, port int, tag string) error {
	site, done, err := ExportStaticBuild(ctx, dk, contextDir, sb)
	if err != nil {
		return err
	}
	defer done()
	dockerfile, remove, err := staticDockerfile(server, port)
	if err != nil {
		return err
	}
	defer remove()
	if out, err := dk.Build(ctx, "-t", tag, "-f", dockerfile, site); err != nil {
		return BuildFailure("packing the static site into the file server's image", out, err)
	}
	return nil
}

// ExportStaticBuild runs a declared static site's build on dk and answers
// its output folder, checked (ConfineStaticExport), and done, which removes
// the export. The build is plain `docker build`: the selected daemon's own
// builder, for its own platform (the site is files).
func ExportStaticBuild(ctx context.Context, dk Scope, contextDir string, sb appcontract.StaticBuild) (site string, done func(), err error) {
	export, err := os.MkdirTemp("", "forge-static-export-")
	if err != nil {
		return "", func() {}, err
	}
	done = func() { _ = os.RemoveAll(export) }
	var args []string
	if sb.Target != "" {
		args = append(args, "--target", sb.Target)
	}
	args = append(args, "--output", "type=local,dest="+export, contextDir)
	if out, err := dk.Build(ctx, args...); err != nil {
		done()
		return "", func() {}, BuildFailure("building the static site", out, err)
	}
	if site, err = ConfineStaticExport(export, sb.Output); err != nil {
		done()
		return "", func() {}, err
	}
	return site, done, nil
}

// ConfineStaticExport is the site folder output names inside an exported
// build stage at root, once nothing in it can lead outside it. The export
// is the project's own build output, so it is read without following a
// link: every folder on the way to output, and everything under it, must
// be a plain folder or file. A link anywhere there is refused, wherever it
// points (built static output holds no links). Links
// elsewhere in the stage are never read.
func ConfineStaticExport(root, output string) (string, error) {
	site := root
	if clean := filepath.Clean(output); clean != "." {
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("the static site's output %q is not inside the build's files", output)
		}
		for _, part := range strings.Split(clean, string(filepath.Separator)) {
			site = filepath.Join(site, part)
			info, err := os.Lstat(site)
			if errors.Is(err, fs.ErrNotExist) {
				return "", fmt.Errorf("the build wrote no %s: the static site's output folder is missing from the built stage's files", output)
			}
			if err != nil {
				return "", err
			}
			if info.Mode()&fs.ModeSymlink != 0 {
				rel, _ := filepath.Rel(root, site)
				return "", fmt.Errorf("the built static site's %s is a symbolic link; a static site's output holds only folders and files", filepath.ToSlash(rel))
			}
			if !info.IsDir() {
				rel, _ := filepath.Rel(root, site)
				return "", fmt.Errorf("the built static site's %s is not a folder", filepath.ToSlash(rel))
			}
		}
	}
	files := 0
	err := filepath.WalkDir(site, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 || !(d.IsDir() || d.Type().IsRegular()) {
			rel, _ := filepath.Rel(root, path)
			return fmt.Errorf("the built static site holds %s, which is not a plain folder or file (a symbolic link is refused, wherever it points)", filepath.ToSlash(rel))
		}
		if !d.IsDir() {
			files++
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if files == 0 {
		return "", fmt.Errorf("the build's %s holds no files: the static site is empty", output)
	}
	return site, nil
}

// staticDockerfile writes the file server's Dockerfile (StaticSiteDockerfile)
// alone in a folder of its own, and answers its path and what removes the
// folder. A Dockerfile outside the build's context is sent from its folder:
// in the shared temporary folder, the builder would list everything other
// work is writing and removing there, and under load lost the file itself.
func staticDockerfile(server string, port int) (string, func(), error) {
	dir, err := os.MkdirTemp("", "forge-static-dockerfile-")
	if err != nil {
		return "", func() {}, err
	}
	remove := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(path, []byte(StaticSiteDockerfile(server, port)), 0o600); err != nil {
		remove()
		return "", func() {}, err
	}
	return path, remove, nil
}
