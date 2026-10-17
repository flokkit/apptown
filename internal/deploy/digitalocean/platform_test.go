package digitalocean

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The small rules the driver applies without the API: the database region
// for an App Platform region, an app's name, the redaction of the token
// from anything the provider or docker says, and reading a status off
// the provider's error.
func TestDriverWords(t *testing.T) {
	if doDBRegion("syd") != "syd1" || doDBRegion("syd1") != "syd1" || doDBRegion("nyc") != "nyc3" {
		t.Fatalf("database regions: %s %s %s", doDBRegion("syd"), doDBRegion("syd1"), doDBRegion("nyc"))
	}
	if doAppName("coin-flip", "cloud") != "coin-flip-cloud" {
		t.Fatal("app name")
	}
	out := redactToken("login failed for dop_v1_secret at registry", "dop_v1_secret")
	if strings.Contains(out, "dop_v1_secret") || !strings.Contains(out, "<token>") {
		t.Fatalf("redaction: %s", out)
	}
	if redactToken("plain", "") != "plain" {
		t.Fatal("an empty token redacts nothing")
	}
	var err error = &APIError{Status: 404, ID: "not_found", Msg: "no such app"}
	if !IsStatus(err, 404) || IsStatus(err, 500) || IsStatus(errors.New("other"), 404) {
		t.Fatal("status off the provider's error")
	}
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "no such app") {
		t.Fatalf("the error keeps the provider's words: %v", err)
	}
	wrapped := fmt.Errorf("listing: %w", err)
	if !IsStatus(wrapped, 404) {
		t.Fatal("a wrapped provider error still answers its status")
	}
}
