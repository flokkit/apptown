package gate

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/internal/trust/trusttest"
)

// The gate redeems tickets with an AppTown whose HTTPS certificate an extra
// authority issued, reading that authority out of the very script every
// other container runs (trust.Set.Script): a bundle of one, and the first
// and the last of a bundle of three. Without the script it cannot redeem.
func TestTheGateTrustsTheExtraAuthoritiesInTheScript(t *testing.T) {
	t.Parallel()
	a, b, c := trusttest.New(t, "First CA"), trusttest.New(t, "Middle CA"), trusttest.New(t, "Last CA")
	script := func(t *testing.T, authorities ...*trusttest.Authority) string {
		var pem []byte
		for _, x := range authorities {
			pem = append(pem, x.PEM()...)
		}
		bundle, err := trust.Parse(pem)
		if err != nil {
			t.Fatal(err)
		}
		set, err := trust.New(t.TempDir(), map[trust.Use]trust.Bundle{trust.Deployments: bundle}, trust.Bundle{})
		if err != nil {
			t.Fatal(err)
		}
		return set.Script(false)
	}
	enter := func(t *testing.T, apptownBy *trusttest.Authority, trustScript string) int {
		apptown := &fakeAppTown{}
		apptown.grant(time.Now().Add(time.Hour))
		port := apptownBy.Serve(t, apptown)
		app := httptest.NewServer(&fakeApp{})
		t.Cleanup(app.Close)
		cfg, err := ConfigFromEnv(func(k string) string {
			return map[string]string{"GATE_UPSTREAM": app.URL, "GATE_SECRET": testSecret,
				"GATE_APPTOWN": fmt.Sprintf("https://localhost:%d", port), trustEnv: trustScript}[k]
		})
		if err != nil {
			t.Fatalf("config: %v", err)
		}
		gate := httptest.NewServer(New(cfg, Options{}))
		t.Cleanup(gate.Close)
		res, err := noRedirects.Get(gate.URL + EnterPath + "?ticket=tk-1")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	for _, tc := range []struct {
		name        string
		by          *trusttest.Authority
		authorities []*trusttest.Authority
	}{
		{"one authority", a, []*trusttest.Authority{a}},
		{"the first of three", a, []*trusttest.Authority{a, b, c}},
		{"the last of three", c, []*trusttest.Authority{a, b, c}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := enter(t, tc.by, script(t, tc.authorities...)); got != http.StatusSeeOther {
				t.Fatalf("entering: %d, want a session (303)", got)
			}
		})
	}
	if got := enter(t, a, ""); got == http.StatusSeeOther {
		t.Fatal("without the script the gate redeemed with an AppTown it cannot trust")
	}
	if _, err := ConfigFromEnv(func(k string) string {
		return map[string]string{"GATE_UPSTREAM": "http://web:8080", "GATE_SECRET": testSecret, "GATE_APPTOWN": "https://forge.example", trustEnv: "echo no certificate"}[k]
	}); err == nil {
		t.Fatal("a trust script with no certificate was accepted")
	}
}
