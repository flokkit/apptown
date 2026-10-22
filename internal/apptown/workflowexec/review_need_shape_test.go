package workflowexec

import (
	"context"
	"strings"
	"testing"
)

// A need parameter that is a map with non-string keys does not parse: the
// agent starts no need services and is told so, since it may be the change
// that fixes the declaration.
func TestReviewNonStringKeyMapsAreNotNeedScalars(t *testing.T) {
	for _, shape := range []string{"database: {1: sourdough}", "properties: {database: {1: sourdough}}"} {
		t.Run(shape, func(t *testing.T) {
			f := agentOnWorkerFixture(t, map[string]any{"needs": true})
			f.call.commit = hubCommit(t, f.hub, map[string]string{".apptown/app.yaml": "app:\n  web:\n    build: .\n    public: true\nneeds:\n  postgres:\n    " + shape + "\n"})
			req, err := f.exec.prepareAgentOnWorker(context.Background(), f.call)
			if err != nil {
				t.Fatal(err)
			}
			if req.spec.Private != nil {
				t.Fatalf("malformed map started services: %+v", req.spec.Private.Services)
			}
			if prompt := req.spec.Argv[len(req.spec.Argv)-1]; !strings.Contains(prompt, ".apptown/app.yaml does not parse") {
				t.Errorf("the agent is not told: %q", prompt)
			}
		})
	}
}
