package inference

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/integration"
)

var failureStatus = regexp.MustCompile(`^(?:HTTP )?([345][0-9]{2})(?:\s|$)|\(HTTP ([345][0-9]{2})\)`)

// Failure describes a provider failure without exposing its raw error text.
// status is the upstream HTTP status (zero when none was received). reported
// is an SDK's error when the caller has no structured status, or empty for a
// model that returned no output and reported no error.
func Failure(provider, model string, status int, reported string) string {
	if provider == "" {
		if binding, _, ok := strings.Cut(model, "/"); ok {
			provider = binding
		}
	}
	title := provider
	// Google is a built-in provider without a connector preset.
	if provider == "google" {
		title = "Google"
	}
	if spec := integration.FindConnector("model", "pi"); spec != nil {
		for _, preset := range spec.Presets {
			if preset.Name == provider {
				title = preset.Title
				break
			}
		}
	}
	if title == "" {
		title = "The model provider"
	}
	if model == "" {
		model = "The model"
	}
	// SDKs prefix the gateway's response with its HTTP status. A transport
	// failure's gateway status is not an upstream internal error.
	if strings.Contains(reported, "AppTown couldn't reach ") {
		return "AppTown couldn't reach " + title
	}
	if status == 0 {
		if match := failureStatus.FindStringSubmatch(reported); match != nil {
			code := match[1]
			if code == "" {
				code = match[2]
			}
			status, _ = strconv.Atoi(code)
		}
	}
	switch {
	case status == 401:
		return title + " refused the API key (HTTP 401)"
	case status == 403:
		return fmt.Sprintf("%s refused access to %s (HTTP 403)", title, model)
	case status == 429:
		return title + " refused the call: rate limit or out of credit (HTTP 429)"
	case status >= 500 && status <= 599:
		return fmt.Sprintf("%s had an internal error (HTTP %d)", title, status)
	case status != 0:
		return fmt.Sprintf("%s refused the call (HTTP %d)", title, status)
	case reported == "":
		return model + " returned nothing"
	}
	lower := strings.ToLower(reported)
	for _, word := range []string{"connection", "timeout", "timed out", "fetch failed", "network", "deadline exceeded"} {
		if strings.Contains(lower, word) {
			return "AppTown couldn't reach " + title
		}
	}
	return title + " refused the call"
}
