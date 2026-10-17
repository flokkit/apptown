package forge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/audit"
)

// The instance secret door is the administrator's alone and write-only:
// a member or an outsider is refused before anything changes; the
// administrator's set is what runtime lookup reads; no answer — the set,
// the names list, the remove, a refusal — carries the value; each change
// leaves a receipt naming who acted on which name.
//
// Kills: the route opened to members (requireRoute with a project
// capability); a response or receipt echoing the value; a set with no
// receipt.
func TestInstanceSecretsAreAdministratorOnlyAndWriteOnly(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	h := a.Handler()
	const value = "dop_v1_instance-secret-keep-out-of-every-answer"
	path := "/api/v1/secrets/DIGITALOCEAN_ACCESS_TOKEN"
	body := `{"value":"` + value + `"}`
	for _, who := range []string{"owner", "outsider", "kid"} {
		for _, method := range []string{"PUT", "DELETE"} {
			if w := authzCall(h, who, method, path, body); w.Code != 403 || strings.Contains(w.Body.String(), value) {
				t.Fatalf("%s %s by %s: %d %s", method, path, who, w.Code, w.Body.String())
			}
		}
	}
	if got := a.Secrets.All(context.Background())["DIGITALOCEAN_ACCESS_TOKEN"]; got != "" {
		t.Fatal("a refused set reached the secrets file")
	}
	var answers []string
	w := authzCall(h, "boss", "PUT", path, body)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"held":true`) {
		t.Fatalf("the administrator's set: %d %s", w.Code, w.Body.String())
	}
	answers = append(answers, w.Body.String())
	if got := a.Secrets.All(context.Background())["DIGITALOCEAN_ACCESS_TOKEN"]; got != value {
		t.Fatal("runtime lookup does not read the value the administrator set")
	}
	w = authzCall(h, "boss", "GET", "/api/v1/secrets/names", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"DIGITALOCEAN_ACCESS_TOKEN":"secrets.env"`) {
		t.Fatalf("names: %d %s", w.Code, w.Body.String())
	}
	answers = append(answers, w.Body.String())
	// A malformed body is refused without being read back.
	w = authzCall(h, "boss", "PUT", path, `{"value":"`+value+`",}`)
	if w.Code != 400 {
		t.Fatalf("a malformed body: %d %s", w.Code, w.Body.String())
	}
	answers = append(answers, w.Body.String())
	w = authzCall(h, "boss", "PUT", "/api/v1/secrets/lower_case", body)
	if w.Code != 400 {
		t.Fatalf("a bad name: %d %s", w.Code, w.Body.String())
	}
	answers = append(answers, w.Body.String())
	w = authzCall(h, "boss", "GET", "/api/v1/audit?limit=50", "")
	answers = append(answers, w.Body.String())
	w = authzCall(h, "boss", "DELETE", path, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"held":false`) {
		t.Fatalf("the administrator's remove: %d %s", w.Code, w.Body.String())
	}
	answers = append(answers, w.Body.String())
	if got := a.Secrets.All(context.Background())["DIGITALOCEAN_ACCESS_TOKEN"]; got != "" {
		t.Fatal("the removed secret still resolves")
	}
	for i, answer := range answers {
		if strings.Contains(answer, value) {
			t.Fatalf("answer %d carries the value: %s", i, answer)
		}
	}
	receipts, err := audit.Recent(context.Background(), a.DB, audit.Filter{KindPrefix: "secret.instance-"})
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 2 || receipts[0].Kind != "secret.instance-removed" || receipts[1].Kind != "secret.instance-set" {
		t.Fatalf("receipts: %+v", receipts)
	}
	for _, r := range receipts {
		if r.Actor != "boss" || r.Subject != "DIGITALOCEAN_ACCESS_TOKEN" || strings.Contains(string(r.Payload), value) {
			t.Fatalf("receipt: %+v", r)
		}
	}
	// Secrets the forge cannot read are unknown, not none: the names list
	// answers unavailable rather than an empty list.
	file := filepath.Join(a.cfg.dataDir, "secrets.env")
	if err := os.RemoveAll(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0o700); err != nil {
		t.Fatal(err)
	}
	if w := authzCall(h, "boss", "GET", "/api/v1/secrets/names", ""); w.Code != 503 || strings.Contains(w.Body.String(), `"names"`) {
		t.Fatalf("names with an unreadable secrets file: %d %s", w.Code, w.Body.String())
	}
}
