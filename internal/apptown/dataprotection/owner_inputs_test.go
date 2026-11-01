package dataprotection_test

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/ids"
)

// The public operation accepts no owner-only fact.
var _ func(*dataprotection.Service, context.Context, ids.ID, string, string) (*dataprotection.Backup, error) = (*dataprotection.Service).TakeBackup
