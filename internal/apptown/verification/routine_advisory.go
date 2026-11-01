package verification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/routinechange"
	"github.com/flokkit/apptown/internal/trust"
)

var routineAdvisoryHTTP = &http.Client{Timeout: 15 * time.Second, Transport: trust.Lazy(trust.AppTown), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
var advisoryID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,255}$`)

func readRoutineAdvisory(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, "https://api.osv.dev/v1/"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := routineAdvisoryHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("advisory service answered HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 2<<20 {
		return nil, errors.New("advisory response exceeds the observation limit")
	}
	return raw, nil
}

func (r routineInspector) observeAdvisories(ctx context.Context, candidate routinechange.NPMCandidate) workflow.StandingAdvisoryObservation {
	out := workflow.StandingAdvisoryObservation{Status: "no-supported-fix-observed"}
	if r.advisoryRead == nil {
		return workflow.StandingAdvisoryObservation{Status: "unavailable", Reason: "advisory observation is unavailable"}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	seen := map[string]bool{}
	pairs := 0
	for i, before := range candidate.Before {
		after := candidate.After[i]
		if !before.Changed || before.Version == after.Version {
			continue
		}
		key := before.Name + "@" + before.Version + "->" + after.Version
		if seen[key] {
			continue
		}
		seen[key] = true
		pairs++
		if pairs > 32 {
			return workflow.StandingAdvisoryObservation{Status: "unsupported", Reason: "too many changed package identities for bounded advisory observation"}
		}
		query, marshalErr := json.Marshal(map[string]any{"package": map[string]string{"ecosystem": "npm", "name": before.Name}, "version": before.Version})
		if marshalErr != nil {
			return workflow.StandingAdvisoryObservation{Status: "unavailable", Reason: "cannot encode advisory query"}
		}
		raw, err := r.advisoryRead(ctx, http.MethodPost, "query", query)
		if err != nil {
			return workflow.StandingAdvisoryObservation{Status: "unavailable", Reason: err.Error()}
		}
		var result *struct{ Vulns []struct{ ID string } }
		if err = json.Unmarshal(raw, &result); err != nil || result == nil {
			return workflow.StandingAdvisoryObservation{Status: "unavailable", Reason: "advisory query was malformed"}
		}
		if len(result.Vulns) > 64 {
			return workflow.StandingAdvisoryObservation{Status: "unsupported", Reason: "too many advisories for bounded observation"}
		}
		ids := map[string]bool{}
		for _, v := range result.Vulns {
			if !advisoryID.MatchString(v.ID) {
				return workflow.StandingAdvisoryObservation{Status: "unavailable", Reason: "advisory query returned an invalid identity"}
			}
			if ids[v.ID] {
				continue
			}
			ids[v.ID] = true
			path := "vulns/" + url.PathEscape(v.ID)
			doc, err := r.advisoryRead(ctx, http.MethodGet, path, nil)
			if err != nil {
				return workflow.StandingAdvisoryObservation{Status: "unavailable", Reason: err.Error()}
			}
			fix, err := routinechange.NPMAdvisoryFix(doc, before.Name, before.Version, after.Version)
			if err != nil {
				out.Reason = "some observed advisory records do not establish a supported fixed transition"
				continue
			}
			if fix.ID != v.ID {
				return workflow.StandingAdvisoryObservation{Status: "unavailable", Reason: "advisory document changed the requested identity"}
			}
			out.Fixes = append(out.Fixes, workflow.StandingAdvisoryFix{ID: fix.ID, Source: "https://api.osv.dev/v1/" + path, Modified: fix.Modified, BodyDigest: digestNPM(doc), Package: fix.Package, Before: fix.Before, After: fix.After})
		}
	}
	if len(out.Fixes) > 0 {
		out.Status = "observed-fixes"
	} else if out.Reason != "" {
		out.Status = "unsupported"
	}
	return out
}
