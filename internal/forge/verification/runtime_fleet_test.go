package verification

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/forge/conformance"
)

func TestPostureModeDeclaration(t *testing.T) {
	t.Parallel()
	if s, ok := check.PostureScript(conformance.CheckDeclaration{Evaluator: "sh", Sh: "exit 0"}); !ok || s != "exit 0" {
		t.Fatal("an sh check must be posture-assessable")
	}
	if _, ok := check.PostureScript(conformance.CheckDeclaration{Evaluator: "llm", Rubric: "x"}); ok {
		t.Fatal("an llm-only check must have no posture mode")
	}
	if evaluatorVersion(conformance.CheckDeclaration{Evaluator: "sh", Sh: "exit 0"}) == "" {
		t.Fatal("an sh posture check must carry an evaluator version")
	}
	if evaluatorVersion(conformance.CheckDeclaration{Evaluator: "llm", Rubric: "x"}) != "" {
		t.Fatal("no posture evaluator → no evaluator version")
	}
}
func TestUndeclaredReadinessIsANonConformance(t *testing.T) {
	t.Parallel()
	observer := Runtime{}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".forge", "app.yaml"), []byte("app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v := observer.Probe(context.Background(), Target{Slug: "x", ContractDir: dir}, "readiness")
	state, detail := v.Outcome, v.Detail
	if state != check.DoesNotConform || !strings.Contains(detail, "health.readiness.http") {
		t.Fatalf("want does-not-conform with the fix in words, got %s: %s", state, detail)
	}
}

// A static part is served by the forge's own file server: it has nothing to
// declare, so its readiness is its root answering through both doors — a
// probe, not a pass by default. A missing index is still a finding.
func TestStaticPartReadinessIsItsRootAnswering(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".forge", "app.yaml"), []byte("app:\n  game:\n    static: .\n    public: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var asked []string
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, r.URL.Path)
		if r.URL.Path != "/" {
			http.NotFound(w, r)
		}
	}))
	defer site.Close()
	v := Runtime{}.Probe(context.Background(), Target{Slug: "x", ContractDir: dir, FrontDoor: site.URL, Naked: site.URL}, "readiness")
	mu.Lock()
	defer mu.Unlock()
	if v.Outcome != check.Conforms || len(asked) != 2 || asked[0] != "/" || asked[1] != "/" {
		t.Fatalf("a static site whose root answers is ready: %s %s (asked %v)", v.Outcome, v.Detail, asked)
	}
	empty := httptest.NewServer(http.NotFoundHandler())
	defer empty.Close()
	if v := (Runtime{}).Probe(context.Background(), Target{Slug: "x", ContractDir: dir, FrontDoor: empty.URL, Naked: empty.URL}, "readiness"); v.Outcome != check.DoesNotConform {
		t.Fatalf("a static site whose root does not answer is a finding: %s %s", v.Outcome, v.Detail)
	}
}
