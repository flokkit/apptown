package adoption

import (
	"context"
	"encoding/json"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption/internal/adoptionsql"
	"github.com/flokkit/apptown/internal/database"
)

// ProjectExport is a project's adoption as it moves to another AppTown: its
// current selection, its revision history verbatim (receipts name the
// source's library ids; they are never resolved elsewhere), and what it has
// installed. Library content travels separately (wow.ExportVersions).
type ProjectExport struct {
	Current         []json.RawMessage `json:"current"`
	Revisions       []json.RawMessage `json:"revisions"`
	Packs           []json.RawMessage `json:"packs"`
	Policies        []json.RawMessage `json:"policies"`
	GuidanceImports []json.RawMessage `json:"guidanceImports"`
	Lanes           []json.RawMessage `json:"lanes"`
	PiExtensions    []json.RawMessage `json:"piExtensions"`
}

// ExportProjectIn reads a project's adoption for its move, in the caller's
// snapshot.
func ExportProjectIn(ctx context.Context, q database.Queryer, projectID ids.ID) (ProjectExport, error) {
	s := adoptionsql.New(q.SQL())
	var out ProjectExport
	for _, part := range []struct {
		into *[]json.RawMessage
		read func(context.Context, ids.ID) ([]string, error)
	}{
		{&out.Current, s.ExportAdoptionCurrent}, {&out.Revisions, s.ExportAdoptionRevisions}, {&out.Packs, s.ExportProjectPacks},
		{&out.Policies, s.ExportPolicies}, {&out.GuidanceImports, s.ExportGuidanceImports}, {&out.Lanes, s.ExportLanes},
		{&out.PiExtensions, s.ExportPiExtensions},
	} {
		rows, err := database.JSONRows(part.read(ctx, projectID))
		if err != nil {
			return ProjectExport{}, err
		}
		*part.into = rows
	}
	return out, nil
}

// CurrentReceipt is the receipt of the revision the project is on, from the
// export itself; ok is false when it has none.
func (e ProjectExport) CurrentReceipt() (Receipt, bool, error) {
	if len(e.Current) == 0 {
		return Receipt{}, false, nil
	}
	var current struct {
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal(e.Current[0], &current); err != nil {
		return Receipt{}, false, err
	}
	for _, raw := range e.Revisions {
		var rev struct {
			Revision int64   `json:"revision"`
			Receipt  Receipt `json:"receipt_json"`
		}
		if err := json.Unmarshal(raw, &rev); err != nil {
			return Receipt{}, false, err
		}
		if rev.Revision == current.Revision {
			return rev.Receipt, true, nil
		}
	}
	return Receipt{}, false, nil
}

// PackVersions are the pack versions the project is placed on, by the
// source library's pack id.
func (e ProjectExport) PackVersions() ([]PackVersion, error) {
	var out []PackVersion
	for _, raw := range e.Packs {
		var row struct {
			PackID  ids.ID `json:"pack_id"`
			Version int64  `json:"version"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		out = append(out, PackVersion{PackID: row.PackID, Version: row.Version})
	}
	return out, nil
}
