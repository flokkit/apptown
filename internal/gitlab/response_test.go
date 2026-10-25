package gitlab

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type responseTransport func(*http.Request) (*http.Response, error)

func (f responseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type failedResponse struct{}

func (failedResponse) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (failedResponse) Close() error             { return nil }

func TestIncompleteGitLabResponsesNeverReportSuccess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   func() io.ReadCloser
	}{
		{"short success", 200, func() io.ReadCloser { return failedResponse{} }},
		{"short absent", 404, func() io.ReadCloser { return failedResponse{} }},
		{"oversized success", 200, func() io.ReadCloser { return io.NopCloser(strings.NewReader(strings.Repeat("x", (1<<20)+1))) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := New("https://gitlab.invalid", "fixture")
			if err != nil {
				t.Fatal(err)
			}
			client.http.Transport = responseTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: tc.body()}, nil
			})
			err = client.DoContext(context.Background(), http.MethodDelete, "/projects/1", nil, nil)
			if err == nil {
				t.Fatal("incomplete response reported success")
			}
			var status *HTTPError
			if errors.As(err, &status) {
				t.Fatalf("incomplete response interpreted as complete HTTP status: %v", err)
			}
		})
	}
}

func TestGitLabRawResponsePreservesStatusAndCompleteBodies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		failed bool
	}{
		{"empty deletion", 204, "", false},
		{"complete absent", 404, `{"message":"absent"}`, false},
		{"exact limit", 200, strings.Repeat("x", 1<<20), false},
		{"over limit", 201, strings.Repeat("x", (1<<20)+1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := New("https://gitlab.invalid", "fixture")
			if err != nil {
				t.Fatal(err)
			}
			client.http.Transport = responseTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			status, body, err := client.Raw(context.Background(), http.MethodDelete, "/projects/1", nil)
			if status != tc.status || (err != nil) != tc.failed {
				t.Fatalf("response status=%d err=%v", status, err)
			}
			if !tc.failed && string(body) != tc.body {
				t.Fatal("complete body changed")
			}
			if tc.failed && len(body) != 0 {
				t.Fatal("incomplete body exposed as usable result")
			}
			if tc.status == 404 {
				err = client.DoContext(context.Background(), http.MethodDelete, "/projects/1", nil, nil)
				var failure *HTTPError
				if !errors.As(err, &failure) || failure.Status != 404 {
					t.Fatalf("complete absent response lost HTTP classification: %v", err)
				}
			}
		})
	}
}

func TestGitLabRawResponseRefusesUnreadableBodiesAndUnencodableRequests(t *testing.T) {
	client, err := New("https://gitlab.invalid", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client.http.Transport = responseTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 404, Body: failedResponse{}}, nil
	})
	status, body, err := client.Raw(context.Background(), http.MethodDelete, "/projects/1", nil)
	if status != 404 || len(body) != 0 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("unreadable response: status=%d body=%q err=%v", status, body, err)
	}
	_, _, err = client.Raw(context.Background(), http.MethodPost, "/projects", make(chan int))
	if err == nil || calls != 1 {
		t.Fatalf("unencodable request sent: calls=%d err=%v", calls, err)
	}
}
