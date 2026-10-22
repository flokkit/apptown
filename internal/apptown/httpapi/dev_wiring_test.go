package httpapi

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// Process mode's one environment carries each part's wired values with the
// localhost need values; two parts wiring one name differently cannot share
// it and are refused, naming the name.
//
// Kills: devWiredEnv not adding wired values; the conflict not refused.
func TestProcessModeGetsWiredValuesInItsOneEnvironment(t *testing.T) {
	doc := "version: 2\napp:\n  web:\n    image: x\n    public: true\n    environment: { CACHE_URL: \"${needs.redis.REDIS_URL}\" }\n  worker:\n    image: x\n    environment: { CACHE_URL: \"${needs.redis.REDIS_URL}\" }\nneeds:\n  redis: {}\n"
	c, err := appcontract.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	env, err := devWiredEnv(c, map[string]string{"REDIS_URL": "redis://localhost:6379"})
	if err != nil || env["CACHE_URL"] != "redis://localhost:6379" || env["REDIS_URL"] != "redis://localhost:6379" {
		t.Fatalf("process env: %v %v", env, err)
	}
	clash, err := appcontract.Parse([]byte(strings.Replace(doc, "worker:\n    image: x\n    environment: { CACHE_URL: \"${needs.redis.REDIS_URL}\" }", "worker:\n    image: x\n    environment: { CACHE_URL: \"${needs.redis.REDIS_URL}/1\" }", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := devWiredEnv(clash, map[string]string{"REDIS_URL": "redis://localhost:6379"}); err == nil || !strings.Contains(err.Error(), "CACHE_URL") {
		t.Fatalf("a clash must be refused naming the name: %v", err)
	}
}
