package adrs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adrs/internal/adrssql"
	"github.com/flokkit/apptown/internal/database"
)

func queries(q database.Queryer) *adrssql.Queries { return adrssql.New(q.SQL()) }

func record(r adrssql.Adr, slug, name string, changeNumber int64) (Record, error) {
	state, err := ParseState(r.Status)
	if err != nil {
		return Record{}, err
	}
	return Record{ID: r.ID, ProjectID: r.ProjectID, ProjectSlug: slug, ProjectName: name,
		Number: r.Number, Title: r.Title, Status: state, StatusLabel: state.display(), BodyMD: r.BodyMd, Tags: r.Tags,
		SupersedesID: r.SupersedesID, ChangeID: r.ChangeID, ChangeNumber: changeNumber,
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: r.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
}
func readRecord(ctx context.Context, db database.Queryer, id, projectID ids.ID, lock bool) (Record, error) {
	arg := adrssql.ADRByIDParams{ID: id, ProjectID: projectID}
	if lock {
		r, e := queries(db).ADRByIDForUpdate(ctx, adrssql.ADRByIDForUpdateParams(arg))
		if e != nil {
			return Record{}, e
		}
		return record(r.Adr, r.Slug, r.Name, r.ChangeNumber)
	}
	r, e := queries(db).ADRByID(ctx, arg)
	if e != nil {
		return Record{}, e
	}
	return record(r.Adr, r.Slug, r.Name, r.ChangeNumber)
}
func readProjectID(ctx context.Context, db database.Queryer, id ids.ID) (ids.ID, error) {
	pid, err := queries(db).ADRScopeMeta(ctx, id)
	return pid, database.Missing(err)
}
func readRecords(ctx context.Context, db database.Queryer, f Filter, idList []ids.ID) ([]Record, error) {
	if f.Limit < 0 || f.Limit > math.MaxInt32 {
		return nil, errInput
	}
	if f.ProjectSlug != "" {
		id, err := project.IDBySlugIn(ctx, db, f.ProjectSlug)
		if errors.Is(err, sql.ErrNoRows) {
			return []Record{}, nil
		}
		if err != nil {
			return nil, err
		}
		if !f.ProjectID.IsZero() && f.ProjectID != id {
			return []Record{}, nil
		}
		f.ProjectID = id
	}
	state := ""
	if f.State != 0 {
		if _, e := f.State.Value(); e != nil {
			return nil, e
		}
		state = f.State.String()
	}
	rows, e := queries(db).ADRsMatching(ctx, adrssql.ADRsMatchingParams{ProjectIds: idList, ProjectID: f.ProjectID, Status: state, Tag: likeTag(f.Tag), Query: likeText(f.Query), ChangeID: f.ChangeID, Recent: f.Recent, LimitCount: int32(f.Limit)})
	if e != nil {
		return nil, e
	}
	out := []Record{}
	for _, r := range rows {
		v, e := record(r.Adr, r.Slug, r.Name, r.ChangeNumber)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func likeTag(s string) string {
	if s == "" {
		return ""
	}
	return "%," + s + ",%"
}
func likeText(s string) string {
	if s == "" {
		return ""
	}
	return "%" + s + "%"
}
func normalizeTags(text string) string {
	var out []string
	for _, tag := range strings.Split(text, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			out = append(out, tag)
		}
	}
	return strings.Join(out, ",")
}
func containsTag(tags, want string) bool {
	for _, tag := range strings.Split(tags, ",") {
		if strings.TrimSpace(tag) == strings.TrimSpace(want) {
			return true
		}
	}
	return false
}

// MatchingIn participates in Work's guarded assertion transaction. Its caller
// owns Work admission and the project lock. Matching ADR rows remain share-locked
// through the Work result/receipt commit; no detached match authorizes a write.
func MatchingIn(ctx context.Context, tx *database.Tx, req MatchRequest) ([]Match, error) {
	if req.ProjectID.IsZero() || req.ChangeID.IsZero() {
		return nil, errInput
	}
	if _, e := req.State.Value(); e != nil {
		return nil, e
	}
	rows, e := queries(tx).ADRsForChangeForShare(ctx, adrssql.ADRsForChangeForShareParams{ProjectID: req.ProjectID, ChangeID: req.ChangeID, Status: req.State.String()})
	if e != nil {
		return nil, e
	}
	out := []Match{}
	for _, r := range rows {
		match := true
		for _, tag := range req.Tags {
			if !containsTag(r.Tags, tag) {
				match = false
				break
			}
		}
		if match {
			out = append(out, Match{ID: r.ID, Number: r.Number, Title: r.Title})
		}
	}
	return out, nil
}

// RemoveProjectIn is the ADR participant in project deletion. The caller owns
// project authorization, its project-first lock, and commit/rollback.
func RemoveProjectIn(ctx context.Context, tx *database.Tx, projectID ids.ID) error {
	if projectID.IsZero() {
		return errInput
	}
	q := queries(tx)
	if e := q.DeleteADRsForProject(ctx, projectID); e != nil {
		return e
	}
	remaining, e := q.ADRsRemainForProject(ctx, projectID)
	if e != nil {
		return e
	}
	if remaining {
		return errConflict
	}
	return nil
}
func readChangeSummaries(ctx context.Context, db database.Queryer, projectID, changeID ids.ID) ([]ChangeSummary, error) {
	rows, e := queries(db).ADRsForChange(ctx, adrssql.ADRsForChangeParams{ProjectID: projectID, ChangeID: changeID})
	if e != nil {
		return nil, e
	}
	out := []ChangeSummary{}
	for _, r := range rows {
		state, e := ParseState(r.Status)
		if e != nil {
			return nil, e
		}
		out = append(out, ChangeSummary{ID: r.ID, Number: r.Number, Title: r.Title, Status: state, StatusLabel: state.display(), Tags: r.Tags})
	}
	return out, nil
}
func requireOne(n int64, err error) error {
	if err != nil {
		return err
	}
	if n != 1 {
		return errConflict
	}
	return nil
}
func requireChange(ctx context.Context, tx *database.Tx, projectID ids.ID, id ids.ID) error {
	if id.IsZero() {
		return nil
	}
	owned, e := queries(tx).ChangeBelongsToProject(ctx, adrssql.ChangeBelongsToProjectParams{ID: id, ProjectID: projectID})
	if e != nil {
		return e
	}
	if !owned {
		return fmt.Errorf("%w: linked Change belongs to another project or is missing", errInput)
	}
	return nil
}
