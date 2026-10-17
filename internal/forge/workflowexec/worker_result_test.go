package workflowexec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/worker/workertest"
)

// An incomplete result response must not pass the step and release its work.
func TestAWorkerResultNamingNoCommitDoesNotPass(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, false)
	ca, state := workertest.NewCA(t), t.TempDir()
	srv := workertest.StartWith(t, ca, state, fake, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/result") {
				next.ServeHTTP(w, r)
				return
			}
			if err := os.WriteFile(filepath.Join(state, "workspaces", taskKeyOf(step), "made.txt"), []byte("work that must not be lost\n"), 0600); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			reply := httptest.NewRecorder()
			next.ServeHTTP(reply, r)
			for name, values := range reply.Header() {
				for _, value := range values {
					w.Header().Add(name, value)
				}
			}
			w.Header().Del("Forge-Head")
			w.WriteHeader(reply.Code)
			_, _ = w.Write(reply.Body.Bytes())
		})
	})
	driver := workertest.Driver(t, ca, srv)
	a.tasks.Driver, a.tasks.Remote, a.tasks.Profile.Name, a.tasks.Backend = driver, driver, "worker", "worker at "+driver.URL
	// The malformed reply is asked for again until the result's patience
	// runs out; a short one keeps the test quick.
	a.tasks.Patience = 2 * time.Second
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	got, err := workflow.ReadStepIn(context.Background(), a.db, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == workflow.StatusPass {
		t.Fatalf("incomplete result passed the step and discarded unpublished work: %s", got.Evidence)
	}
}
