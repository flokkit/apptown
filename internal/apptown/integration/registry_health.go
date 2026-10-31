package integration

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

// TestContainerRegistryBinding asks the registry's API with this connector's
// stored credential. It never retries anonymously or follows redirects.
func (s *Service) TestContainerRegistryBinding(ctx context.Context, config map[string]any) Health {
	host, _ := config["url"].(string)
	username, _ := config["username"].(string)
	u, err := url.Parse("https://" + host)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return Health{Detail: "Give the registry host without a scheme or path."}
	}
	// Docker's tools read docker.io as Docker Hub's API host; docker.io
	// itself only redirects to the website.
	if u.Host == "docker.io" {
		u.Host = "registry-1.docker.io"
	}
	token, err := s.registryToken(ctx, ConfigStrings(config["tokenFrom"]))
	if err != nil {
		return Health{Detail: "Registry credentials could not be read."}
	}
	if username == "" || token == "" {
		return Health{Detail: "Set the registry username and token secret before checking it."}
	}
	u.Path = "/v2/"
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Health{Detail: "The registry address could not be read."}
	}
	req.SetBasicAuth(username, token)
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return Health{Detail: "The registry could not be reached."}
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return Health{OK: true, Detail: "Registry reachable; credentials accepted."}
	case http.StatusUnauthorized:
		if params := registryBearerChallenge(resp.Header.Values("WWW-Authenticate")); params != nil && params["realm"] != "" {
			return checkRegistryTokenService(ctx, client, u, params, username, token)
		}
		return Health{Detail: "Registry credentials were refused. Check the username and token secret."}
	default:
		return Health{Detail: fmt.Sprintf("Registry GET /v2/ returned %d.", resp.StatusCode)}
	}
}

// Parse auth parameters with the MIME quoted-string parser. Authentication uses
// commas between parameters where MIME uses semicolons; quoted commas stay intact.
func registryBearerChallenge(headers []string) map[string]string {
	for _, header := range headers {
		scheme, parameters, found := strings.Cut(strings.TrimSpace(header), " ")
		if !found || !strings.EqualFold(scheme, "Bearer") {
			continue
		}
		var normalized strings.Builder
		quoted, escaped := false, false
		for _, c := range parameters {
			if c == ',' && !quoted {
				c = ';'
			}
			normalized.WriteRune(c)
			if escaped {
				escaped = false
			} else if c == '\\' && quoted {
				escaped = true
			} else if c == '"' {
				quoted = !quoted
			}
		}
		_, params, err := mime.ParseMediaType("Bearer;" + normalized.String())
		if err == nil {
			return params
		}
	}
	return nil
}

func checkRegistryTokenService(ctx context.Context, client *http.Client, registry *url.URL, params map[string]string, username, token string) Health {
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Scheme != "https" || realm.Hostname() == "" || realm.User != nil || realm.Fragment != "" {
		return Health{StateLabel: HealthUnknown, Detail: "Token service requires a valid HTTPS address; not checked."}
	}
	registryDomain, registryErr := publicsuffix.EffectiveTLDPlusOne(strings.ToLower(registry.Hostname()))
	realmDomain, realmErr := publicsuffix.EffectiveTLDPlusOne(strings.ToLower(realm.Hostname()))
	if registryErr != nil || realmErr != nil || registryDomain != realmDomain {
		return Health{StateLabel: HealthUnknown, Detail: "Token service on another domain; not checked."}
	}
	query := realm.Query()
	query.Set("service", params["service"])
	query.Set("account", username)
	realm.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return Health{Detail: "The token service address could not be read."}
	}
	req.SetBasicAuth(username, token)
	resp, err := client.Do(req)
	if err != nil {
		return Health{Detail: "The registry token service could not be reached."}
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return Health{OK: true, Detail: "Registry token service reachable; credentials accepted."}
	case http.StatusUnauthorized, http.StatusForbidden:
		return Health{Detail: "Registry credentials were refused by the token service. Check the username and token secret."}
	default:
		return Health{Detail: fmt.Sprintf("Registry token service returned %d.", resp.StatusCode)}
	}
}
