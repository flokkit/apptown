package repositoryedit

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/appcontract"
	"gopkg.in/yaml.v3"
)

// A contract with everything the services editor must not touch: a public
// app with per-part fields, an image worker, a raw sidecar, init containers,
// and needs carrying typed values and `properties:`.
const richContract = `app:
  web:
    build: .
    ports: ["8080"]
    public: true
    environment: {K: v}
    dataDir: /data
    healthcheck: "wget -qO- localhost:8080/health || exit 1"
  worker:
    image: worker:1
    command: [work, "--queue", jobs]
    environment: {WORKER_ONLY: "1"}
  mail:
    image: axllent/mailpit
init:
  - {name: migrate, build: ., command: ["./migrate.sh"]}
needs:
  postgres:
    version: 16
    properties: {extensions: pgvector}
  redis: {version: "7"}
`

func writeContract(t *testing.T, body string) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".apptown", "app.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

func tree(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		t.Fatalf("yaml: %v\n%s", err, b)
	}
	return m
}

// The form sends back what the GET gave it (parsed, stringified params) plus
// the person's edits. Only the needs they changed may change; the app, init
// and every untouched need entry stay as written, types and properties too.
func TestNeedEditsLeaveTheAppAndOtherNeedsAsWritten(t *testing.T) {
	t.Parallel()
	repo := writeContract(t, richContract)
	c, err := appcontract.Parse([]byte(richContract))
	if err != nil {
		t.Fatal(err)
	}
	asRead := func() []NeedSelection {
		var out []NeedSelection
		for _, n := range c.Needs {
			params := map[string]any{}
			for k, v := range c.NeedParams[n] {
				params[k] = v
			}
			out = append(out, NeedSelection{Capability: n, Params: params})
		}
		return out
	}
	before := tree(t, []byte(richContract))

	// A round trip of what was read changes nothing and opens nothing.
	if _, err := editDeclaration(repo, asRead()); !errors.Is(err, ErrNeedsUnchanged) {
		t.Fatalf("a no-op edit must be refused as unchanged: %v", err)
	}

	// Changing Redis's version leaves Postgres exactly as written.
	picks := asRead()
	for i := range picks {
		if picks[i].Capability == "redis" {
			picks[i].Params["version"] = "8"
		}
	}
	out, err := editDeclaration(repo, picks)
	if err != nil {
		t.Fatal(err)
	}
	after := tree(t, out)
	for _, key := range []string{"app", "init"} {
		if !reflect.DeepEqual(after[key], before[key]) {
			t.Fatalf("%s changed:\nbefore %v\nafter  %v", key, before[key], after[key])
		}
	}
	needs := after["needs"].(map[string]any)
	if !reflect.DeepEqual(needs["postgres"], before["needs"].(map[string]any)["postgres"]) {
		t.Fatalf("an untouched need was rewritten: %v", needs["postgres"])
	}
	if needs["redis"].(map[string]any)["version"] != "8" {
		t.Fatalf("the changed version was not written: %v", needs["redis"])
	}

	// Removing Redis removes only Redis.
	out, err = editDeclaration(repo, picks[:1])
	if err != nil {
		t.Fatal(err)
	}
	after = tree(t, out)
	if !reflect.DeepEqual(after["app"], before["app"]) || !reflect.DeepEqual(after["needs"], map[string]any{"postgres": before["needs"].(map[string]any)["postgres"]}) {
		t.Fatalf("removing one need touched more:\n%s", out)
	}

	// Adding a need writes its params as sent.
	out, err = editDeclaration(repo, append(asRead(), NeedSelection{Capability: "s3", Params: map[string]any{"bucket": "files"}}))
	if err != nil {
		t.Fatal(err)
	}
	if got := tree(t, out)["needs"].(map[string]any)["s3"]; !reflect.DeepEqual(got, map[string]any{"bucket": "files"}) {
		t.Fatalf("new need: %v", got)
	}

	if _, err := editDeclaration(repo, append(asRead(), NeedSelection{Capability: "redis"})); err == nil {
		t.Fatal("a need sent twice must be refused")
	}
}

func TestFirstNeedSynthesizesAContractAndNothingIsRefused(t *testing.T) {
	t.Parallel()
	out, err := editDeclaration(t.TempDir(), []NeedSelection{{Capability: "redis"}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := appcontract.Parse(out)
	if err != nil {
		t.Fatalf("synthesized app.yaml must be valid: %v\n%s", err, out)
	}
	if c.PublicPart().Build != "." || c.PublicPart().Port != 8080 || len(c.Needs) != 1 || c.Needs[0] != "redis" {
		t.Fatalf("synthesized contract: %+v", c)
	}
	if _, err := editDeclaration(t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "no runtime contract") {
		t.Fatalf("declaring nothing on a contract-less project must refuse: %v", err)
	}
	if _, err := editDeclaration(writeContract(t, "app: [this is not a map]\n\t bad: : :"), []NeedSelection{{Capability: "redis"}}); err == nil {
		t.Fatal("a malformed existing app.yaml must be rejected")
	}
}

// A parameter kept under properties: is edited where it lives, since the
// contract reads properties first; the edit takes effect.
func TestEditingAParameterKeptInPropertiesTakesEffect(t *testing.T) {
	t.Parallel()
	repo := writeContract(t, "app:\n  web: {image: nginx, ports: [\"8080\"], public: true}\nneeds:\n  postgres:\n    properties: {version: \"16\", database: app}\n")
	out, err := editDeclaration(repo, []NeedSelection{{Capability: "postgres", Params: map[string]any{"version": "17", "database": "app"}}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := appcontract.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.NeedParams["postgres"]["version"]; got != "17" {
		t.Fatalf("successful edit still deploys version %s:\n%s", got, out)
	}
}
