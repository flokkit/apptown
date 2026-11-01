package worker

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/execution"
)

// brokenBody fails partway through, as a connection cut mid-answer does.
type brokenBody struct{ io.Reader }

func (b brokenBody) Read(p []byte) (int, error) {
	if n, _ := b.Reader.Read(p); n > 0 {
		return n, nil
	}
	return 0, errors.New("connection reset")
}
func (brokenBody) Close() error { return nil }

// A session refusal is read by the one worker refusal reader: one whose
// body cannot be read says nothing certain, so it is the worker
// unavailable, never "no such session". A whole refusal keeps its class.
func TestASessionRefusalThatCannotBeReadIsTheWorkerUnavailable(t *testing.T) {
	t.Parallel()
	cut := &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Header: http.Header{}, Body: brokenBody{strings.NewReader("no such")}}
	if err := refusal(cut); !errors.Is(err, execution.ErrWorkerUnavailable) || errors.Is(err, errNoSession) {
		t.Fatalf("a refusal cut short: %v", err)
	}
	whole := &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Header: http.Header{}, Body: io.NopCloser(strings.NewReader("no such session"))}
	if err := refusal(whole); !errors.Is(err, errNoSession) || !strings.Contains(err.Error(), "no such session") {
		t.Fatalf("a whole refusal: %v", err)
	}
}
