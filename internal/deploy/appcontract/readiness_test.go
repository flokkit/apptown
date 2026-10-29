package appcontract

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadyIsBelow400OrTheDeclaredStatus(t *testing.T) {
	for _, c := range []struct {
		status, expect int
		ready          bool
	}{
		{0, 0, false}, {200, 0, true}, {204, 0, true}, {301, 0, true}, {399, 0, true},
		{400, 0, false}, {404, 0, false}, {500, 0, false},
		{404, 404, true}, {200, 404, false}, {0, 404, false}, {301, 200, false},
	} {
		if got := Ready(c.status, c.expect); got != c.ready {
			t.Errorf("Ready(%d, %d) = %v, want %v", c.status, c.expect, got, c.ready)
		}
	}
}

func TestSampleReadinessJudgesOneAnswerWithoutFollowingRedirects(t *testing.T) {
	var host, header string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, header = r.Host, r.Header.Get("X-Forwarded-Proto")
		switch r.URL.Path {
		case "/missing":
			w.WriteHeader(404)
		case "/moved":
			http.Redirect(w, r, "/missing", http.StatusFound)
		case "/release":
			_, _ = w.Write([]byte("r1"))
		default:
			_, _ = w.Write([]byte("ok"))
		}
	}))
	defer srv.Close()
	ask := func(r ReadinessRequest) ReadinessAnswer {
		t.Helper()
		r.URL = srv.URL + r.URL
		a, err := SampleReadiness(context.Background(), srv.Client().Transport, r)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	if a := ask(ReadinessRequest{URL: "/missing"}); a.Ready || a.Status != 404 {
		t.Errorf("a 404 with nothing declared = %+v, want not ready", a)
	}
	if a := ask(ReadinessRequest{URL: "/missing", Expect: 404}); !a.Ready {
		t.Errorf("a declared 404 = %+v, want ready", a)
	}
	if a := ask(ReadinessRequest{URL: "/moved"}); !a.Ready || a.Status != http.StatusFound {
		t.Errorf("a redirect = %+v, want its own 302, not followed, ready", a)
	}
	if a := ask(ReadinessRequest{URL: "/release", Release: "r2"}); a.Ready {
		t.Errorf("a marker that names another release = %+v, want not ready", a)
	}
	if a := ask(ReadinessRequest{URL: "/release", Release: "r1"}); !a.Ready {
		t.Errorf("the marker of this release = %+v, want ready", a)
	}
	ask(ReadinessRequest{URL: "/", Host: "app.example", Headers: map[string]string{"X-Forwarded-Proto": "https"}})
	if host != "app.example" || header != "https" {
		t.Errorf("sent Host %q and X-Forwarded-Proto %q, want app.example and https", host, header)
	}
	if _, err := SampleReadiness(context.Background(), nil, ReadinessRequest{URL: "http://127.0.0.1:1/"}); err == nil {
		t.Error("no answer is an error")
	}
}
