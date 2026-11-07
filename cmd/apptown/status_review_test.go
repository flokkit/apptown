package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

func TestReviewShippedLineUsesServerStatus(t *testing.T) {
	t.Parallel()
	var deps []shippedDeployment
	err := json.Unmarshal([]byte(`[{"name":"main","url":"https://shop.example","observation":"running","observationStale":false,"observedAt":"2026-11-07T00:00:00Z","status":{"word":"unknown","reason":"s3: its host's container listing could not be read: it did not answer in time","checkedAt":"2026-11-07T00:00:00Z","stale":true,"retryAt":"2026-11-07T00:00:20Z"}}]`), &deps)
	if err != nil {
		t.Fatal(err)
	}
	line := deploymentsLine(deps, ids.Nil)
	if !strings.Contains(line, "unknown") || !strings.Contains(line, "s3") || strings.Contains(line, "(running)") {
		t.Fatalf("shipped line disagrees with server status: %s", line)
	}
}

// The API timing and freshness fields reach the shipped line as well as the word.
func TestShippedLineDecodesServerStatusTiming(t *testing.T) {
	t.Parallel()
	var deps []shippedDeployment
	if err := json.Unmarshal([]byte(`[{"name":"main","status":{"word":"unknown","reason":"s3 did not answer in time","checkedAt":"2026-11-07T00:12:37Z","stale":true,"retryAt":"2026-11-07T00:12:57Z"}}]`), &deps); err != nil {
		t.Fatal(err)
	}
	line := deploymentsLine(deps, ids.Nil)
	checked := time.Date(2026, 11, 7, 0, 12, 37, 0, time.UTC)
	for _, want := range []string{"unknown — s3 did not answer in time", "old reading", "checked " + checked.Local().Format("15:04"), "next check " + checked.Add(20*time.Second).Local().Format("15:04")} {
		if !strings.Contains(line, want) {
			t.Errorf("shipped line lacks %q: %s", want, line)
		}
	}
}
