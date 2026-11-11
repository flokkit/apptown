package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
)

func (d *Driver) ImageStore(ctx context.Context, _ deploy.Placement) (string, error) {
	dk, err := d.Scope(ctx)
	if err != nil {
		return "", err
	}
	return dk.ImageStore(ctx)
}

// ImageStore is the daemon's own identity, shared by direct and proxy access.
func (dk Scope) ImageStore(ctx context.Context) (string, error) {
	raw, err := dk.Run(ctx, "info", "--format", "{{.ID}}")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(raw))
	if id == "" {
		return "", fmt.Errorf("Docker answered no image store identity")
	}
	return "docker/" + id, nil
}

func (d *Driver) TrimImages(ctx context.Context, _ deploy.Placement, expired, keep []string) ([]string, []deploy.Left, error) {
	dk, err := d.Scope(ctx)
	if err != nil {
		return nil, nil, err
	}
	return dk.TrimImages(ctx, expired, keep, d.BuildTagPrefix)
}

// TrimImages lists recorded images and owned landing build tags before effects.
// Removing a reference never forces removal of an image used by a container.
func (dk Scope) TrimImages(ctx context.Context, expired, keep []string, buildPrefix string) ([]string, []deploy.Left, error) {
	raw, err := dk.Run(ctx, "image", "ls", "--no-trunc", "--format", "{{json .}}")
	if err != nil {
		return nil, nil, fmt.Errorf("listing Docker images: %w", err)
	}
	type image struct{ ID, Repository, Tag string }
	var rows []image
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var img image
		if json.Unmarshal([]byte(line), &img) != nil || img.ID == "" || img.Repository == "" {
			return nil, nil, fmt.Errorf("Docker answered an unreadable image list")
		}
		rows = append(rows, img)
	}
	all := append(slices.Clone(expired), keep...)
	present, err := recordedImages(ctx, dk, all)
	if err != nil {
		return nil, nil, err
	}
	keepIDs := map[string]bool{}
	for _, ref := range keep {
		if !slices.Contains(present, ref) {
			continue
		}
		out, err := dk.Run(ctx, "image", "inspect", "--format", "{{.Id}}", ref)
		if err != nil {
			return nil, nil, err
		}
		id := strings.TrimSpace(string(out))
		if !strings.HasPrefix(id, "sha256:") {
			return nil, nil, fmt.Errorf("Docker answered no image ID for %s", ref)
		}
		keepIDs[id] = true
	}
	candidates := []string{}
	for _, ref := range expired {
		if slices.Contains(present, ref) && !slices.Contains(keep, ref) {
			candidates = append(candidates, ref)
		}
	}
	if buildPrefix != "" {
		for _, img := range rows {
			if strings.HasPrefix(img.Repository, buildPrefix+"preview-build-build-") && !keepIDs[img.ID] {
				candidates = append(candidates, img.Repository+":"+img.Tag)
			}
		}
	}
	candidates = slices.Compact(slices.Sorted(slices.Values(candidates)))
	var deleted []string
	var left []deploy.Left
	for _, ref := range candidates {
		used, err := dk.RemoveImageUnlessUsed(ctx, ref)
		if err != nil {
			left = append(left, deploy.Left{What: ref, Why: err.Error()})
		} else if !used {
			deleted = append(deleted, ref)
		}
	}
	return deleted, left, nil
}

func (dk Scope) HasImages(ctx context.Context, refs []string) (bool, error) {
	held, err := recordedImages(ctx, dk, refs)
	return len(held) == len(slices.Compact(slices.Sorted(slices.Values(refs)))), err
}
func (d *Driver) HasImages(ctx context.Context, _ deploy.Placement, refs []string) (bool, error) {
	scope, err := d.Scope(ctx)
	if err != nil {
		return false, err
	}
	return scope.HasImages(ctx, refs)
}
