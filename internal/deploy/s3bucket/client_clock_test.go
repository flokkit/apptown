package s3bucket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A request's signature is dated by the client's clock.
func TestARequestIsSignedAtTheClientsTime(t *testing.T) {
	t.Parallel()
	dated := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dated <- r.Header.Get("X-Amz-Date")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := &Client{Endpoint: srv.URL, Region: "syd1", AccessKey: "a", SecretKey: "s", http: srv.Client(),
		Now: func() time.Time { return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC) }}
	if err := c.DeleteObject(context.Background(), "bucket", "key"); err != nil {
		t.Fatal(err)
	}
	if got := <-dated; got != "20130524T000000Z" {
		t.Fatalf("signed at %q, not the client's time", got)
	}
}
