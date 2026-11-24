package deployment

import (
	"context"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/imagelines"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/imageline"
)

// NeedsBehind reads the running deployment's unpinned lines for the Janitor.
// Provider-managed and stack-managed needs have no Docker storage bindings.
func (s *Service) NeedsBehind(ctx context.Context, dep *Deployment) ([]string, error) {
	bindings, err := s.storageBindings(ctx, dep.ID)
	if err != nil || len(bindings) == 0 {
		return nil, err
	}
	inputs, found, err := s.serving(ctx, dep)
	if err != nil || !found {
		return nil, err
	}
	project, err := s.release.Sources.Project(ctx, dep.ProjectID)
	if err != nil {
		return nil, err
	}
	c, _, err := s.release.Sources.Contract(ctx, project, inputs.Revision)
	if err != nil {
		return nil, err
	}
	var reasons []string
	for _, b := range bindings {
		kind, err := s.env.needs.ByCapability(ctx, b.Need)
		if err != nil {
			return nil, err
		}
		line, pin := kind.ImageLine(c.NeedParams[b.Need])
		if line == "" || pin {
			continue
		}
		reading, err := s.env.images.Answer(ctx, line, imageline.Line)
		if err != nil {
			return nil, err
		}
		if needBehind(b.DeclaredImage, b.RepoDigest, line, reading) {
			old, _ := imageline.Parse(b.DeclaredImage, imageline.Line)
			next, _ := imageline.Parse(reading.Tag, imageline.Line)
			running := old.Prefix
			l, _ := imageline.Parse(line, imageline.Line)
			if running == l.Prefix {
				running = "unknown"
			}
			reasons = append(reasons, fmt.Sprintf("%s %s is available (runs %s)", b.Need, next.Prefix, running))
		}
	}
	return reasons, nil
}

func needBehind(bound, digest, line string, reading imagelines.Reading) bool {
	_, oldDigest, _ := strings.Cut(digest, "@")
	if oldDigest == reading.Digest {
		return false
	}
	boundary, e := imageline.Parse(line, imageline.Line)
	old, oe := imageline.Parse(bound, imageline.Line)
	next, ne := imageline.Parse(reading.Tag, imageline.Line)
	return e == nil && oe == nil && ne == nil && old.Repository.Name() == boundary.Repository.Name() && next.Repository.Name() == boundary.Repository.Name() && old.Variant == boundary.Variant && next.Variant == boundary.Variant && imageline.Allows(line, bound, line, imageline.Line) == nil && imageline.Allows(bound, reading.Tag, line, imageline.Line) == nil
}

func (s *Service) needContract(ctx context.Context, dep *Deployment) (*appcontract.Contract, error) {
	inputs, found, err := s.serving(ctx, dep)
	if err != nil || !found {
		return nil, err
	}
	project, err := s.release.Sources.Project(ctx, dep.ProjectID)
	if err != nil {
		return nil, err
	}
	c, _, err := s.release.Sources.Contract(ctx, project, inputs.Revision)
	return c, err
}
func (s *Service) cachedNeedUpdates(ctx context.Context, dep *Deployment) (map[string]string, error) {
	out := map[string]string{}
	if s.env.images == nil {
		return out, nil
	}
	bindings, err := s.storageBindings(ctx, dep.ID)
	if err != nil || len(bindings) == 0 {
		return out, err
	}
	c, err := s.needContract(ctx, dep)
	if err != nil || c == nil {
		return out, err
	}
	for _, b := range bindings {
		kind, err := s.env.needs.ByCapability(ctx, b.Need)
		if err != nil {
			return nil, err
		}
		line, pin := kind.ImageLine(c.NeedParams[b.Need])
		if line == "" || pin {
			continue
		}
		reading, known, err := s.env.images.Cached(ctx, line, imageline.Line)
		if err != nil {
			return nil, err
		}
		if known && needBehind(b.DeclaredImage, b.RepoDigest, line, reading) {
			next, _ := imageline.Parse(reading.Tag, imageline.Line)
			out[b.Need] = next.Prefix
		}
	}
	return out, nil
}
