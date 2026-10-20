package httpapi

import (
	"encoding/json"
	"flokkit.io/forge/internal/deploy/appcontract"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestBothDevComposeModesUseTheCatalogueSize(t *testing.T) {
	h := configurationFixture(t)
	c, err := appcontract.Parse([]byte("app: {web: {image: nginx, public: true}}\nneeds: {mariadb: {size: small}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, generate := range []func(*appcontract.Contract) (string, error){func(c *appcontract.Contract) (string, error) { return h.generateDevCompose(t.Context(), c) }, func(c *appcontract.Contract) (string, error) { return h.generateNeedsCompose(t.Context(), c) }} {
		out, err := generate(c)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"mem_limit: 134217728", "memswap_limit: 134217728", "--innodb-buffer-pool-size=32M"} {
			if !strings.Contains(out, want) {
				t.Fatalf("compose missing %s: %s", want, out)
			}
		}
	}
	if len(c.NeedResources) != 0 {
		t.Fatal("compose changed source")
	}
}
func TestConfigValidationChecksSizesInEveryProfile(t *testing.T) {
	h := configurationFixture(t)
	body, _ := json.Marshal(map[string]string{"appYaml": "app: {web: {image: nginx, public: true}}\nneeds: {mariadb: {}}\nprofiles: {preview: {mariadb: {size: nonexistent}}}\n"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/validate", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	h.ConfigValidate(w, req)
	var result ContractCheck
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.OK || !strings.Contains(w.Body.String(), "nonexistent") {
		t.Fatalf("unknown non-default size validated: %s", w.Body.String())
	}
}

// The contract check refuses a contract's needs as a release would: an
// integration under needs: and a password AppTown sets.
func TestConfigValidationRefusesNeedsAsDeclared(t *testing.T) {
	h := configurationFixture(t)
	for yaml, want := range map[string]string{
		"needs: {errors: {}}\n":                   "errors is an integration, not a need",
		"needs: {mariadb: {password: chosen}}\n":  "AppTown sets the password of the mariadb need",
		"needs: {postgres: {database: ledger}}\n": "",
	} {
		body, _ := json.Marshal(map[string]string{"appYaml": "app: {web: {image: nginx, public: true}}\n" + yaml})
		w := httptest.NewRecorder()
		h.ConfigValidate(w, httptest.NewRequest(http.MethodPost, "/api/v1/config/validate", strings.NewReader(string(body))))
		var result ContractCheck
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.OK != (want == "") || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %s", yaml, w.Body.String())
		}
	}
}

// Both development modes give a need that keeps data a folder there (an
// anonymous volume at the kind's data path), as AppTown's own starts do:
// S3's emulator exits at once without /data. A need with none gets none.
func TestBothDevComposeModesGiveANeedItsDataFolder(t *testing.T) {
	h := configurationFixture(t)
	// No stock kind keeps no data, so a custom one stands for it.
	if _, err := h.catalog.Save(t.Context(), "capability: scratch\nlocal:\n  image: scratch:1\n", "bls"); err != nil {
		t.Fatal(err)
	}
	c, err := appcontract.Parse([]byte("app: {web: {image: nginx, public: true}}\nneeds: {s3: {}, scratch: {}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	for name, generate := range map[string]func(*appcontract.Contract) (string, error){
		"container": func(c *appcontract.Contract) (string, error) { return h.generateDevCompose(t.Context(), c) },
		"process":   func(c *appcontract.Contract) (string, error) { return h.generateNeedsCompose(t.Context(), c) },
	} {
		out, err := generate(c)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Services map[string]struct {
				Volumes    []string `yaml:"volumes"`
				Entrypoint []string `yaml:"entrypoint"`
				Command    []string `yaml:"command"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatal(err)
		}
		if got := doc.Services["s3"].Volumes; len(got) != 1 || got[0] != "/data" {
			t.Errorf("%s mode: s3 has volumes %v, want [/data]: %s", name, got, out)
		}
		// S3 starts by its script, whose shell variables Compose must not
		// interpolate: they are written $$.
		if s3 := doc.Services["s3"]; len(s3.Entrypoint) != 2 || s3.Entrypoint[0] != "/bin/sh" || len(s3.Command) != 1 || !strings.Contains(s3.Command[0], `$$gateway`) {
			t.Errorf("%s mode: s3 is not started by its script: %+v", name, s3)
		}
		if got, ok := doc.Services["scratch"]; !ok || len(got.Volumes) != 0 {
			t.Errorf("%s mode: scratch (no data path) has volumes %v, present %v", name, got.Volumes, ok)
		}
	}
}
