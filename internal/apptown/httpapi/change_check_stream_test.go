package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/poll/polltest"
)

func TestChangeCheckFlushesProgressBeforeItsResult(t *testing.T) {
	t.Parallel()
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "result", true: "error after progress"}[fails], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), polltest.HangGuard(t))
			defer cancel()
			read := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				streamChangeCheck(w, r, func(ctx context.Context, report func(string)) (map[string]any, error) {
					report("first check")
					select {
					case <-read:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					if fails {
						return nil, errors.New("checker failed")
					}
					return map[string]any{"ok": true, "commit": "abc"}, nil
				})
			}))
			defer srv.Close()
			req, err := http.NewRequestWithContext(ctx, "POST", srv.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			res, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.Header.Get("Content-Type") != "application/x-ndjson" {
				t.Fatal(res.Header)
			}
			dec := json.NewDecoder(res.Body)
			var first map[string]any
			if err := dec.Decode(&first); err != nil {
				t.Fatal(err)
			}
			if first["progress"] != "first check" {
				t.Fatal(first)
			}
			close(read) // The checker cannot return until the real client has this line.
			var last map[string]any
			if err := dec.Decode(&last); err != nil {
				t.Fatal(err)
			}
			if fails && last["error"] != "checker failed" || !fails && last["ok"] != true {
				t.Fatal(last)
			}
		})
	}
}
