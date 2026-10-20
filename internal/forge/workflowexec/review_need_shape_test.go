package workflowexec

import (
	"context"
	"testing"
)

func TestReviewNonStringKeyMapsAreNotNeedScalars(t *testing.T) {
	for _, shape := range []string{"database: {1: sourdough}", "properties: {database: {1: sourdough}}"} {
		t.Run(shape, func(t *testing.T) {
			f := agentOnWorkerFixture(t, map[string]any{"needs": true})
			f.call.commit = hubCommit(t, f.hub, map[string]string{".apptown/app.yaml": "app:\n  web:\n    build: .\n    public: true\nneeds:\n  postgres:\n    " + shape + "\n"})
			req, err := f.exec.prepareAgentOnWorker(context.Background(), f.call)
			if err == nil {
				t.Fatalf("malformed map was admitted: services=%+v", req.spec.Private.Services)
			}
			t.Logf("refusal: %v", err)
		})
	}
}
