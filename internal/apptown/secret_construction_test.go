package apptown

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type secretHTTPClientFunc func(*http.Request) (*http.Response, error)

func (f secretHTTPClientFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

// Identical provider configuration must not cause one App to use another's
// transport or cached secrets. Both Apps exist before either resolves a value.
func TestSecretProviderClientAndCacheBelongToTheirApp(t *testing.T) {
	t.Parallel()
	var apps []*App
	var calls [2]int
	for i := range 2 {
		client := secretHTTPClientFunc(func(r *http.Request) (*http.Response, error) {
			calls[i]++
			var body string
			switch r.URL.Path {
			case "/api/v1/auth/universal-auth/login":
				body = `{"accessToken":"fixture","expiresIn":3600}`
			case "/api/v3/secrets/raw":
				body = fmt.Sprintf(`{"secrets":[{"secretKey":"TOKEN","secretValue":"app-%d"}]}`, i)
			default:
				return nil, fmt.Errorf("unexpected provider request: %s", r.URL.Path)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		a := testApp(t, Config{SecretsHTTPClient: client})
		const env = "INFISICAL_URL=https://secret.invalid\nINFISICAL_CLIENT_ID=fixture\nINFISICAL_CLIENT_SECRET=fixture\nINFISICAL_PROJECT_ID=fixture\n"
		if err := os.WriteFile(filepath.Join(a.cfg.dataDir, "secrets.env"), []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
		apps = append(apps, a)
	}
	for range 2 {
		for i, a := range apps {
			values, err := a.Secrets.InfisicalValues(t.Context())
			if err != nil || values["TOKEN"] != fmt.Sprintf("app-%d", i) {
				t.Fatalf("App %d resolved another transport's value: %v (%v)", i, values, err)
			}
		}
	}
	if calls != [2]int{2, 2} {
		t.Fatalf("each App should authenticate and fetch once, then use its own cache: %v", calls)
	}
}
