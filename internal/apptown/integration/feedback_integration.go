package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/strs"
)

// Customer-feedback integration: the Type-5 embed contract graduated to a
// capability (design/architecture/contracts.md). Attach "feedback" to a project and the
// AppTown provisions an app key on the Customer Feedback service; the key and
// URL are injected into app containers, and agents are steered with the
// exact one-line embed. Same shape as the errors capability: connector
// (where the service lives) → provision (mint/adopt the key) → contribute
// (env + steering).

const CapFeedback = "feedback"

type feedbackBinding struct {
	Name string
	URL  string
}

// feedbackBindingNamed is the named feedback connector (the first when
// name is ""), nil when there is none with a url; a read that fails is an
// error.
func (s *Service) feedbackBindingNamed(ctx context.Context, name string) (*feedbackBinding, error) {
	b, found, err := s.Bindings.FirstNamed(ctx, CapFeedback, name)
	if err != nil || !found {
		return nil, err
	}
	cfg, err := strictJSONMap(b.ConfigJSON)
	if err != nil {
		return nil, fmt.Errorf("the feedback connector %s's configuration: %w", b.Name, err)
	}
	url, _ := cfg["url"].(string)
	if url == "" {
		return nil, nil
	}
	return &feedbackBinding{Name: b.Name, URL: strings.TrimSuffix(url, "/")}, nil
}

func feedbackReq(method, url string, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, raw, nil
}

// TestFeedbackBinding: the service answers and lists apps.
func TestFeedbackBinding(config map[string]any) Health {
	url, _ := config["url"].(string)
	if url == "" {
		return Health{OK: false, Detail: "url is required"}
	}
	code, _, err := feedbackReq("GET", strings.TrimSuffix(url, "/")+"/api/apps", nil)
	if err != nil {
		return Health{OK: false, Detail: "unreachable: " + err.Error()}
	}
	if code != 200 {
		return Health{OK: false, Detail: fmt.Sprintf("GET /api/apps → %d", code)}
	}
	return Health{OK: true, Detail: "service reachable"}
}

type feedbackApp struct {
	Name string `json:"Name"`
	Key  string `json:"Key"`
}

// ProvisionFeedback: adopt the app registration if one already carries this
// project's name, else create it; store {appKey, url} as the resource.
func (s *Service) ProvisionFeedback(ctx context.Context, p *project.Project, connector, who string) (map[string]any, int, error) {
	if err := refusesIntegrations(ctx, s.Bindings.db, p.ID); err != nil {
		return nil, 422, err
	}
	b, err := s.feedbackBindingNamed(ctx, connector)
	if err != nil {
		return nil, 503, err
	}
	if b == nil {
		return nil, 422, fmt.Errorf("no feedback connector%s — add the Customer feedback integration first", nameSuffix(connector))
	}

	appName := p.Name
	if appName == "" {
		appName = p.Slug
	}
	var key string
	if code, raw, err := feedbackReq("GET", b.URL+"/api/apps", nil); err == nil && code == 200 {
		var apps []feedbackApp
		if json.Unmarshal(raw, &apps) == nil {
			for _, ap := range apps {
				if ap.Name == appName && ap.Key != "" {
					key = ap.Key
					break
				}
			}
		}
	}
	if key == "" {
		code, raw, err := feedbackReq("POST", b.URL+"/api/apps", map[string]string{"name": appName})
		if err != nil {
			return nil, 502, fmt.Errorf("registering app: %w", err)
		}
		if code != 200 && code != 201 {
			return nil, 502, fmt.Errorf("registering app: HTTP %d: %s", code, strs.Truncate(string(raw), 200))
		}
		var ap feedbackApp
		if json.Unmarshal(raw, &ap) != nil || ap.Key == "" {
			return nil, 502, fmt.Errorf("app registration returned no key")
		}
		key = ap.Key
	}
	// The provider made it: its attachment is recorded though ctx has ended.
	if err := s.Attachments.attach(context.WithoutCancel(ctx), p.ID, CapFeedback, b.Name, encodeResource(map[string]string{"appKey": key, "url": b.URL}), Provisioned, who, map[string]any{"app": appName}); err != nil {
		return nil, 500, err
	}
	return map[string]any{"ok": true, "capability": CapFeedback, "connector": b.Name,
		"provenance": "provisioned", "app": appName, "review": b.URL + "/"}, 200, nil
}
