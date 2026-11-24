package imagelines

import (
	"context"
	"errors"
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/imageline"
	"github.com/flokkit/apptown/stock"
	"strings"
	"testing"
	"time"
)

type needReadings struct{ staticReadings }

func (r *needReadings) Answer(ctx context.Context, ref string, scope imageline.Scope) (Reading, error) {
	r.asked = append(r.asked, ref)
	if r.refused != nil {
		return Reading{}, r.refused
	}
	return Reading{Answer: imageline.Answer{Tag: "postgres:16.16-alpine", Digest: "sha256:" + strings.Repeat("b", 64)}, ReadAt: time.Now()}, nil
}
func TestBundlesReadUnpinnedNeedsAndRefuseAnUnreadableLine(t *testing.T) {
	t.Parallel()
	catalog := dependency.NewCatalog(suite.New(t), nil)
	if _, _, err := catalog.SyncStock(t.Context(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"16", "16.15"} {
		c := parseContract(t, "needs:\n  postgres: {version: \""+version+"\"}\napp:\n  web: {image: app:1, public: true}\n")
		r := &needReadings{}
		images, logs, err := Needs(t.Context(), r, catalog, c)
		if err != nil {
			t.Fatal(err)
		}
		if version == "16.15" {
			if len(images) != 0 || len(r.asked) != 0 {
				t.Fatal("pin read a newer build")
			}
			continue
		}
		if images["postgres"] != "postgres:16.16-alpine@sha256:"+strings.Repeat("b", 64) || len(logs) != 1 || len(r.asked) != 1 || r.asked[0] != "postgres:16-alpine" {
			t.Fatalf("readings: %v %v %v", images, logs, r.asked)
		}
		r.refused = errors.New("registry offline")
		if _, _, err := Needs(t.Context(), r, catalog, c); err == nil || !strings.Contains(err.Error(), "postgres:16-alpine") || !strings.Contains(err.Error(), "registry offline") {
			t.Fatalf("failed reading: %v", err)
		}
	}
}
