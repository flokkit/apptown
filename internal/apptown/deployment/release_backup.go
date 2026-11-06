package deployment

import (
	"context"
	"encoding/json"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids"
)

// releaseData takes the pre-release copy and keeps continuity distinct from
// the later scheduled backup. A nil collaborator is an explicitly unconfigured
// standalone owner; assembly supplies data protection for every release.
type releaseData interface {
	BeforeRelease(context.Context, ids.ID, string, bool) (ReleaseWitness, error)
	AfterRelease(ids.ID, string)
}
type ReleaseWitness interface {
	Complete() bool
	CopyEvidence() json.RawMessage
	BackupID() ids.ID
	CopyWords() string
	After(context.Context, []string) ([]string, []string)
}

// releaseRollbacks uses data protection's stopped restore after release approval.
type releaseRollbacks interface {
	ReleaseBefore(context.Context, ids.ID, string, ids.ID) (int64, error)
	RestoreForRelease(context.Context, ids.ID, string, ids.ID, ids.ID, access.Principal) error
}
