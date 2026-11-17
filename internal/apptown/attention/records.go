package attention

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/ids"
)

type requestRecordsKey struct{}
type RequestRecords struct {
	Projects map[ids.ID]project.Project
	Targets  []*deployment.Target
}

// WithRecords shares the authorized request's project and target records.
func WithRecords(ctx context.Context, records RequestRecords) context.Context {
	return context.WithValue(ctx, requestRecordsKey{}, records)
}
func readProjects(ctx context.Context, reader interface {
	ByIDs(context.Context, []ids.ID) (map[ids.ID]project.Project, error)
}, ids []ids.ID) (map[ids.ID]project.Project, error) {
	if records, ok := ctx.Value(requestRecordsKey{}).(RequestRecords); ok {
		return records.Projects, nil
	}
	return reader.ByIDs(ctx, ids)
}
func readTargets(ctx context.Context, reader interface {
	All(context.Context) ([]*deployment.Target, error)
}) ([]*deployment.Target, error) {
	if records, ok := ctx.Value(requestRecordsKey{}).(RequestRecords); ok {
		return records.Targets, nil
	}
	return reader.All(ctx)
}
