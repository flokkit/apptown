package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/jobs"
	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
)

func TestJobListRefusesUnreadableOutput(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	if err := jobs.Migrate(context.Background(), db.Pool()); err != nil {
		t.Fatal(err)
	}
	q, err := jobs.New(jobs.Options{}, db.Pool(), map[string]jobs.Kind{"inventory": {MaxAttempts: 1, Run: func(context.Context, string, map[string]any) (map[string]any, error) { return nil, nil }}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := q.Enqueue(context.Background(), "inventory", "host", nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := riverdatabasesql.New(db.Pool()).GetExecutor().JobUpdateFull(context.Background(), &riverdriver.JobUpdateFullParams{ID: id, MetadataDoUpdate: true, Metadata: []byte(`{"output":[]}`)}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/api/v1/jobs", nil)
	response := httptest.NewRecorder()
	NewJobs(db).ListJobs(response, request)
	if response.Code != 500 || !strings.Contains(response.Body.String(), "reading job") {
		t.Fatalf("unreadable result hidden: %d %s", response.Code, response.Body.String())
	}
}
