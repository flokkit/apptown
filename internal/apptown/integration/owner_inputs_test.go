package integration_test

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/ids"
)

// The public operation accepts no owner-only fact.
var _ func(*integration.Attachments, context.Context, ids.ID, string, string, string, string, map[string]any) error = (*integration.Attachments).Attach
