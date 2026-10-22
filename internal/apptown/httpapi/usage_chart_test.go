package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/ids"
)

// The HTTP chart carries the requested window and the owner's daily rollups.
// Exact timestamp grouping and persisted attribution are protected in Usage.
func TestUsageHTTPReturnsDailySeries(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	ledger := usage.NewService(db, instanceReader{}, nil)
	if err := ledger.Ledger.Record(context.Background(), usage.Entry{Kind: "workflow", Actor: "bls", Provider: "p", Model: "m"}, usage.Totals{Input: 10, Output: 5, Total: 15, CostMicros: 100}, ""); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	reader := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "reader"}, Verified: true})
	NewUsage(ledger, project.New(db, nil)).Usage(w, httptest.NewRequest("GET", "/api/v1/usage?window=7", nil).WithContext(reader))
	if w.Code != 200 {
		t.Fatalf("usage: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		WindowDays int `json:"windowDays"`
		ByDay      []struct {
			Day        string `json:"day"`
			Total      int64  `json:"total"`
			CostMicros int64  `json:"costMicros"`
		} `json:"byDay"`
		ByDayKind []struct {
			Day        string `json:"day"`
			Kind       string `json:"kind"`
			Total      int64  `json:"total"`
			CostMicros int64  `json:"costMicros"`
		} `json:"byDayKind"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.WindowDays != 7 || len(out.ByDay) != 1 || len(out.ByDayKind) != 1 {
		t.Fatalf("daily chart: %s", w.Body.String())
	}
	day, kind := out.ByDay[0], out.ByDayKind[0]
	if _, err := time.Parse("2006-01-02", day.Day); err != nil {
		t.Fatalf("calendar day %q: %v", day.Day, err)
	}
	if day.Total != 15 || day.CostMicros != 100 || kind.Day != day.Day || kind.Kind != "workflow" || kind.Total != 15 || kind.CostMicros != 100 {
		t.Fatalf("daily amounts: %s", w.Body.String())
	}
}

// instanceReader reads the whole instance; this test is about the chart.
type instanceReader struct{}

func (instanceReader) Require(ctx context.Context, p access.Permission) (access.Caller, error) {
	return access.Caller{}, nil
}
func (instanceReader) VisibleProjects(context.Context, access.Capability) ([]ids.ID, error) {
	return nil, nil
}
