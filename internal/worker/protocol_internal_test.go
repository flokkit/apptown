package worker

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// A worker built before task observations said whether Docker killed a task
// at its memory limit serves protocol 1 only: it refuses this AppTown by name
// (426), so an out-of-memory kill on it is never read as an ordinary exit.
// A worker of this build still serves an AppTown of the previous one.
//
// Kills: protocol 1 kept while the observation changed meaning (review
// CT-C05).
func TestAWorkerThatCannotReportMemoryKillsRefusesThisAppTown(t *testing.T) {
	t.Parallel()
	ok := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	ask := func(h http.Handler, protocol int) int {
		r := httptest.NewRequest("GET", "/v1/tasks/k", nil)
		r.Header.Set(protocolHeader, strconv.Itoa(protocol))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if code := ask(servedRange(1, 1, ok), protocol); code != http.StatusUpgradeRequired {
		t.Fatalf("an old worker served this AppTown: %d", code)
	}
	if code := ask(served(ok), protocol-1); code != http.StatusOK {
		t.Fatalf("this worker refused the previous AppTown: %d", code)
	}
}
