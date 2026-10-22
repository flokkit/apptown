package wow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/wow/internal/wowsql"
	"github.com/flokkit/apptown/internal/database"
)

// VersionRef names one library version a moving project uses. Library
// numbering is this AppTown's own, so the version's content travels with it.
type VersionRef struct {
	Kind    string // guidance | workflow | pack
	Name    string // guidance and workflow: by name
	PackID  ids.ID // pack: by this AppTown's id
	Version int64
}

// ExportedVersion is one library version's item and content, as stored.
type ExportedVersion struct {
	Kind    string          `json:"kind"`
	Content json.RawMessage `json:"content"`
}

// ExportVersionsIn reads the library versions a moving project uses. A
// version this AppTown does not hold is said by name, never skipped.
func ExportVersionsIn(ctx context.Context, q database.Queryer, refs []VersionRef) ([]ExportedVersion, error) {
	s := wowsql.New(q.SQL())
	out := make([]ExportedVersion, 0, len(refs))
	for _, r := range refs {
		var row string
		var err error
		switch r.Kind {
		case "guidance":
			row, err = s.ExportGuidanceVersion(ctx, wowsql.ExportGuidanceVersionParams{Name: r.Name, Version: r.Version})
		case "workflow":
			row, err = s.ExportWorkflowVersion(ctx, wowsql.ExportWorkflowVersionParams{Name: r.Name, Version: r.Version})
		case "pack":
			row, err = s.ExportPackVersion(ctx, wowsql.ExportPackVersionParams{ID: r.PackID, Version: r.Version})
		default:
			return nil, fmt.Errorf("unknown library kind %q", r.Kind)
		}
		if found, lookupErr := database.Lookup(err); lookupErr != nil {
			return nil, lookupErr
		} else if !found {
			name := r.Name
			if r.Kind == "pack" {
				name = fmt.Sprintf("#%d", r.PackID)
			}
			return nil, fmt.Errorf("the library has no %s %s version %d", r.Kind, name, r.Version)
		}
		out = append(out, ExportedVersion{Kind: r.Kind, Content: json.RawMessage(row)})
	}
	return out, nil
}
