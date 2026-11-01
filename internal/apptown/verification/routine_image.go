package verification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/oci"
	"github.com/flokkit/apptown/internal/routinechange"
)

func eligibleImageContract(before, after []byte) error {
	if !bytes.Equal(before, after) {
		return errors.New("the application contract changed with the base image")
	}
	c, err := appcontract.Parse(before)
	if err != nil {
		return fmt.Errorf("reading the application contract: %w", err)
	}
	if len(c.Parts) != 1 || len(c.Init) != 0 || len(c.Needs) != 0 || len(c.Profiles) != 0 {
		return errors.New("base image inspection requires one root app build without needs, init or profile overrides")
	}
	p := c.Parts[0]
	if p.Build != "." || p.Image != "" || p.IsStatic() || p.DataDir != "" {
		return errors.New("base image inspection cannot classify alternate builds, image declarations or durable app data")
	}
	return nil
}

var nodeVersionObserver = []byte(`const fs = require('node:fs');
fs.writeFileSync('/work/observation.json', JSON.stringify({node:process.versions.node, os:process.platform, arch:process.arch, observer:'node-version-v1'}));
`)

func (r routineInspector) validateImage(ctx context.Context, candidate routinechange.ImageCandidate) (out []workflow.StandingImageObservation, err error) {
	if r.imageReader == nil || r.executor.Image == nil || r.executor.Run == nil {
		return nil, errors.New("image observation is unavailable")
	}
	observations := make([]oci.Observation, 2)
	refs := []string{candidate.Before, candidate.After}
	for i, ref := range refs {
		observations[i], err = r.imageReader(ctx, oci.Source{Reference: ref})
		if err != nil {
			return nil, fmt.Errorf("observing base image: %w", err)
		}
		v := observations[i]
		if v.Repository != "index.docker.io/library/node" && v.Repository != "docker.io/library/node" {
			return nil, errors.New("image observation changed the official repository")
		}
		// The exact manifest is independent of a tag's current resolution.
		if !bytes.HasSuffix([]byte(ref), []byte("@"+v.Manifest)) || v.Platform != candidate.Platform {
			return nil, errors.New("image observation differs from the requested digest or platform")
		}
	}
	if observations[0].Repository != observations[1].Repository || !bytes.Equal(observations[0].Config, observations[1].Config) {
		return nil, errors.New("image runtime configuration changed")
	}
	staging := filepath.Join(r.dataDir, "staging")
	if err = os.MkdirAll(staging, 0700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(staging, "routine-image-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if e := os.RemoveAll(dir); e != nil {
			err = errors.Join(err, fmt.Errorf("removing image observation staging: %w", e))
		}
	}()
	for i, ref := range refs {
		prepared := r.executor.Image(ctx, execution.ImageRequest{Tag: ref, Pull: ref, Anonymous: true, Scratch: dir, Timeout: 2 * time.Minute})
		observed := observations[i]
		if prepared.Err != "" || prepared.NotBuilt || prepared.ID == "" || prepared.ID != observed.ConfigDigest {
			return nil, fmt.Errorf("prepared image differs from the observed config: %s", prepared.Err)
		}
		side := filepath.Join(dir, fmt.Sprint(i))
		if err = os.Mkdir(side, 0700); err != nil {
			return nil, err
		}
		result := r.executor.Run(ctx, execution.Request{Name: "apptown-" + filepath.Base(dir) + fmt.Sprint(i), Workspace: side, Image: prepared.ID, Argv: []string{"env", "-i", "PATH=/usr/local/bin:/usr/bin:/bin", "node", "/routine-image.cjs"}, NoEntrypoint: true, Network: "none", Timeout: time.Minute, Report: "observation.json", Files: []execution.File{{Path: "/routine-image.cjs", Content: nodeVersionObserver, Mode: 0444}}, Limits: execution.DefaultTaskLimits})
		if !result.Exited0() || result.RunErr != nil || result.ReportErr != nil || result.CleanupErr != nil {
			return nil, fmt.Errorf("image version observation did not complete: termination=%s exit=%d: %w", result.Termination, result.ExitCode, errors.Join(result.RunErr, result.ReportErr, result.CleanupErr))
		}
		var report struct{ Node, OS, Arch, Observer string }
		if err = json.Unmarshal(result.Report, &report); err != nil {
			return nil, fmt.Errorf("reading image version observation: %w", err)
		}
		arch := report.Arch
		if arch == "x64" {
			arch = "amd64"
		}
		platform, e := oci.Platform(report.OS, arch)
		if e != nil || platform != candidate.Platform || report.Node != candidate.Version || report.Observer != "node-version-v1" {
			return nil, errors.New("observed runtime version or platform differs from the declared image")
		}
		scratch := "local staging removal required before return"
		if !r.executor.Local {
			scratch = "worker scratch removal is unobserved here and retried by its sweep"
		}
		out = append(out, workflow.StandingImageObservation{Reference: ref, Manifest: observed.Manifest, ConfigDigest: observed.ConfigDigest, PreparedImage: prepared.ID, Node: report.Node, Platform: platform, ReportDigest: digestNPM(result.Report), ScratchSettlement: scratch})
	}
	return out, nil
}
