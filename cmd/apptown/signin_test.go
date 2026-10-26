package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSignInConfigurationIsExplicitAndCarriesOnlyASecretReference(t *testing.T) {
	file := filepath.Join(t.TempDir(), "provider.json")
	if e := os.WriteFile(file, []byte(`{"revision":0,"issuer":"https://issuer.example","clientId":"client","displayName":"Example","secretRef":"OIDC_SECRET","allowSignup":false,"initialGroup":""}`), 0600); e != nil {
		t.Fatal(e)
	}
	seen := jsonAppTown(t, func(method, path string) (int, string) { return 200, `{"revision":1}` })
	if _, e := run(t, "signin", "enable"); e == nil || len(*seen) != 0 {
		t.Fatalf("implicit enable: %v %+v", e, *seen)
	}
	if _, e := run(t, "signin", "configure", "--file", file); e != nil {
		t.Fatal(e)
	}
	if len(*seen) != 1 || (*seen)[0].Method != "PUT" || (*seen)[0].Path != "/auth/external/provider" || (*seen)[0].Body["secretRef"] != "OIDC_SECRET" {
		t.Fatalf("configuration changed: %+v", *seen)
	}
	if _, e := run(t, "signin", "enable", "--revision", "1"); e != nil {
		t.Fatal(e)
	}
	if len(*seen) != 2 || (*seen)[1].Body["revision"] != float64(1) || (*seen)[1].Body["enabled"] != true {
		t.Fatalf("enable lost revision: %+v", *seen)
	}
}
func TestSignInAdministrationRefusalDoesNotClaimSuccess(t *testing.T) {
	seen := jsonAppTown(t, func(string, string) (int, string) { return 409, `{"error":"configuration changed"}` })
	out, e := run(t, "signin", "disable", "--revision", "2")
	if e == nil || !strings.Contains(e.Error(), "changed") || out != "" || len(*seen) != 1 {
		t.Fatalf("false success: %q %v %+v", out, e, *seen)
	}
}
