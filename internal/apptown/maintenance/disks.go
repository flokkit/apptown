package maintenance

import (
	"context"
	"fmt"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/toolversions"
	"path/filepath"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/diskspace"
)

// Disks reads the filesystems this AppTown writes to (its data folder: the
// database's when it is built in, repositories, workspaces, caches,
// backups kept here) and those of its worker (its state folder, and the
// Docker its tasks run on), each its own reading: different filesystems
// are never summed into one figure. A deployment target's own disk is the
// target's capacity (deployment.Service.Capacity). Background checks collect
// each reading with its own time; one that cannot be read says why.
type Disks struct {
	Docker *docker.Scope
	Tools  toolversions.Source

	DataDir        string
	WorkerIdentity string
	// Worker reads the worker's own disks (execution.WorkerTasks.Disks);
	// nil for an AppTown with no worker.
	Worker interface {
		Disks(context.Context) ([]diskspace.Reading, error)
	}
	// Space reads the data folder's filesystem; nil reads it
	// (diskspace.Statfs). A test scene supplies its own reading.
	Space func(path string) (diskspace.Space, error)
	Now   func() time.Time
}

// read collects the controller and worker filesystems under the background job.
// Worker failure stays typed so the observer can retain earlier evidence.
func (d Disks) read(ctx context.Context) (diskspace.Reading, []diskspace.Reading, error) {
	now := clock.Or(d.Now)
	statfs := d.Space
	if statfs == nil {
		statfs = diskspace.Statfs
	}
	space, err := statfs(d.DataDir)
	own := diskspace.NewReading("this AppTown's data folder", d.DataDir, space, err, now())
	// Controller admission is not protected by this advisory observation.
	own.Refuses, own.FloorBytes = false, 0
	own.Categories = []diskspace.Category{diskspace.Folder(ctx, "Workspaces", filepath.Join(d.DataDir, "workspaces"), now()), diskspace.Folder(ctx, "Local backups", filepath.Join(d.DataDir, "backups"), now())}

	if d.Worker == nil {
		return own, nil, nil
	}
	worker, err := d.Worker.Disks(ctx)
	return own, worker, err
}

func (d Disks) local(ctx context.Context) []diskspace.Reading {
	now := clock.Or(d.Now)
	var local []diskspace.Reading
	if d.Docker != nil && !d.Docker.Absent() {
		root, err := d.Docker.Run(ctx, "info", "--format", "{{.DockerRootDir}}")
		var space diskspace.Space
		rootPath := strings.TrimSpace(string(root))
		if err == nil && (rootPath == "" || strings.ContainsAny(rootPath, "\n:")) {
			err = fmt.Errorf("Docker did not report a valid data folder")
		}
		if err == nil {
			image := ""
			if d.Tools != nil {
				image = d.Tools.Current().Images.DF
			}
			space, err = diskspace.ReadThroughDocker(ctx, func(ctx context.Context, args ...string) (string, string, error) {
				raw, err := d.Docker.Run(ctx, args...)
				return string(raw), "", err
			}, image, strings.TrimSpace(string(root)), now)
		}
		r := diskspace.NewReading("this AppTown's Docker data", strings.TrimSpace(string(root)), space, err, now())
		r.Refuses, r.FloorBytes = false, 0
		r.Categories = diskspace.DockerCategories(ctx, d.Docker.Run, now())
		local = append(local, r)
	}
	return local
}
