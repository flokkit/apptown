package imagelines

import (
	"context"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/imageline"
)

// Needs reads unpinned catalogue engine lines once, when a bundle is built.
func Needs(ctx context.Context, r Resolver, catalog *dependency.Catalog, c *appcontract.Contract) (map[string]string, []string, error) {
	images := map[string]string{}
	var evidence []string
	for _, need := range c.Needs {
		if catalog == nil {
			return nil, nil, fmt.Errorf("reading %s: no dependency catalogue", need)
		}
		kind, err := catalog.ByCapability(ctx, need)
		if err != nil {
			return nil, nil, err
		}
		line, pin := kind.ImageLine(c.NeedParams[need])
		if line == "" || pin {
			continue
		}
		if r == nil {
			return nil, nil, fmt.Errorf("reading %s: no image registry readings", line)
		}
		reading, err := r.Answer(ctx, line, imageline.Line)
		if err != nil {
			return nil, nil, fmt.Errorf("reading %s: %w", line, err)
		}
		images[need] = reading.Image()
		evidence = append(evidence, fmt.Sprintf("%s: %s (%s)", need, reading.Image(), reading.Evidence()))
	}
	return images, evidence, nil
}
