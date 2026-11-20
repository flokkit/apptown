package imagelines

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/imageline"
)

// StaticServer is c as it is served. A contract whose static parts name no
// staticServer: is given the newest build on appcontract.StaticServerLine
// that this instance reads (scope line), as tag@digest, with the reading's
// evidence; everything after this step treats it as it treats a pin. A pin,
// or a contract without static parts, is returned unchanged with no
// evidence. A failed reading is an error naming the line: nothing is
// guessed.
func StaticServer(ctx context.Context, r Resolver, c *appcontract.Contract) (*appcontract.Contract, string, error) {
	if c == nil || !c.HasStaticParts() || c.StaticServer != "" {
		return c, "", nil
	}
	if r == nil {
		return nil, "", fmt.Errorf("serving the static parts needs a reading of %s, and this AppTown has no image registry readings", appcontract.StaticServerLine)
	}
	reading, err := r.Answer(ctx, appcontract.StaticServerLine, imageline.Line)
	if err != nil {
		return nil, "", fmt.Errorf("serving the static parts needs the newest %s: %w", appcontract.StaticServerLine, err)
	}
	image := reading.Image()
	if err := appcontract.ValidateStaticServer(image); err != nil {
		return nil, "", fmt.Errorf("the newest %s read from the registry: %w", appcontract.StaticServerLine, err)
	}
	served := *c
	served.StaticServer = image
	return &served, reading.Evidence(), nil
}
