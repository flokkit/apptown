package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

// ProjectExport is a project's Work as it moves to another AppTown: every
// row whole, as
// the source holds it. The receiving AppTown gives each row a new id; nothing
// here is evidence on that AppTown.
type ProjectExport struct {
	Changes         []json.RawMessage `json:"changes"`
	Runs            []json.RawMessage `json:"runs"`
	Continuations   []json.RawMessage `json:"continuations"`
	Landings        []json.RawMessage `json:"landings"`
	Steps           []json.RawMessage `json:"steps"`
	RunEvents       []json.RawMessage `json:"runEvents"`
	Artifacts       []json.RawMessage `json:"artifacts"`
	InferenceOwners []json.RawMessage `json:"inferenceOwners"`
	Transcripts     []json.RawMessage `json:"transcripts"`
}

// ExportProjectIn reads a project's Work for its move, in the caller's
// snapshot.
func ExportProjectIn(ctx context.Context, q database.Queryer, projectID ids.ID) (ProjectExport, error) {
	s := workflowsql.New(q.SQL())
	var out ProjectExport
	for _, part := range []struct {
		into *[]json.RawMessage
		read func(context.Context, ids.ID) ([]string, error)
	}{
		{&out.Changes, s.ExportChanges}, {&out.Runs, s.ExportRuns}, {&out.Continuations, s.ExportContinuations},
		{&out.Landings, s.ExportLandings}, {&out.Steps, s.ExportSteps}, {&out.RunEvents, s.ExportRunEvents},
		{&out.Artifacts, s.ExportArtifacts}, {&out.InferenceOwners, s.ExportRunInferenceOwners},
		{&out.Transcripts, s.ExportAgentTranscripts},
	} {
		rows, err := database.JSONRows(part.read(ctx, projectID))
		if err != nil {
			return ProjectExport{}, err
		}
		*part.into = rows
	}
	return out, nil
}

// Counts is how many of each kind of row the export holds.
func (e ProjectExport) Counts() map[string]int {
	return map[string]int{"changes": len(e.Changes), "runs": len(e.Runs), "steps": len(e.Steps), "runEvents": len(e.RunEvents),
		"artifacts": len(e.Artifacts), "agentTranscripts": len(e.Transcripts)}
}

// ChangeIDs are the exported changes' idList, for the files kept beside them.
func (e ProjectExport) ChangeIDs() ([]ids.ID, error) {
	idList := make([]ids.ID, 0, len(e.Changes))
	for _, raw := range e.Changes {
		var row struct {
			ID ids.ID `json:"id"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		idList = append(idList, row.ID)
	}
	return idList, nil
}

// TranscriptDigests are the kept transcripts the exported receipts name,
// each once.
func (e ProjectExport) TranscriptDigests() ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, raw := range e.Transcripts {
		var row struct {
			Digest string `json:"digest"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		if row.Digest != "" && !seen[row.Digest] {
			seen[row.Digest] = true
			out = append(out, row.Digest)
		}
	}
	return out, nil
}

// ExportTranscript reads one kept transcript for a move, checked against its
// digest; a missing or damaged file is said, never replaced.
func ExportTranscript(dataDir, digest string) ([]byte, error) {
	if !validDigest(digest) {
		return nil, fmt.Errorf("%q is not a transcript's digest", digest)
	}
	return readTranscriptFile(transcriptPath(dataDir, digest), digest)
}

// ExportAttachments are a change's attachments, by name, for a move. A
// change with no attachment folder has none; a folder or file that cannot
// be read is an error, never an empty answer.
func ExportAttachments(dataDir string, changeID ids.ID) (map[string][]byte, error) {
	dir := GitWorkspace{DataDir: dataDir}.AttachmentDir(changeID)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return map[string][]byte{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out[e.Name()] = b
	}
	return out, nil
}

// ExportCommitsIn are the commits a project's Work rows name: a move must
// carry each of them, or its evidence would point at nothing.
func ExportCommitsIn(ctx context.Context, q database.Queryer, projectID ids.ID) ([]string, error) {
	return workflowsql.New(q.SQL()).ExportNamedCommits(ctx, projectID)
}

// ExportLandedCommitsIn are the commits the moving landings merged into the
// default branch: each must be on the bundled default branch.
func ExportLandedCommitsIn(ctx context.Context, q database.Queryer, projectID ids.ID) ([]string, error) {
	return workflowsql.New(q.SQL()).ExportLandedCommits(ctx, projectID)
}

// ExportSkip is what stays behind when a project moves: a change that has
// not landed (open or abandoned) with its runs, chats, refs and
// attachments, or a run still running.
type ExportSkip struct {
	Kind  string `json:"kind"` // change | run
	ID    ids.ID `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

// ExportSkippedIn lists what stays behind: only landed history moves.
func ExportSkippedIn(ctx context.Context, q database.Queryer, projectID ids.ID) ([]ExportSkip, error) {
	rows, err := workflowsql.New(q.SQL()).ExportSkipped(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]ExportSkip, len(rows))
	for i, r := range rows {
		out[i] = ExportSkip{Kind: r.Kind, ID: r.ID, Title: r.Title, State: r.State}
	}
	return out, nil
}

// RunIDs are the exported runs' idList, for the refs that move with them.
func (e ProjectExport) RunIDs() ([]ids.ID, error) {
	idList := make([]ids.ID, 0, len(e.Runs))
	for _, raw := range e.Runs {
		var row struct {
			ID ids.ID `json:"id"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		idList = append(idList, row.ID)
	}
	return idList, nil
}

// Branches are the exported changes' branches.
func (e ProjectExport) Branches() ([]string, error) {
	var out []string
	for _, raw := range e.Changes {
		var row struct {
			Branch string `json:"branch"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		if row.Branch != "" {
			out = append(out, row.Branch)
		}
	}
	return out, nil
}

// ExportStayingRunsIn are the runs that stay when a project moves: running,
// or on a change not landed.
func ExportStayingRunsIn(ctx context.Context, q database.Queryer, projectID ids.ID) ([]ids.ID, error) {
	return workflowsql.New(q.SQL()).ExportStayingRuns(ctx, projectID)
}
