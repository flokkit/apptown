package worker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"github.com/flokkit/apptown/internal/execution"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

type reviewS2Run func(context.Context, string, execution.Request) execution.Outcome

func (f reviewS2Run) Run(c context.Context, b string, r execution.Request) execution.Outcome {
	return f(c, b, r)
}

func TestACheckRunsOnlyOnceItsRequestIsRead(t *testing.T) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	spec, _ := form.CreateFormField("spec")
	json.NewEncoder(spec).Encode(CheckSpec{Image: "x", Argv: []string{"true"}, TimeoutSeconds: 1})
	tree, _ := form.CreateFormFile("tree", "tree.tar")
	tar.NewWriter(tree).Close()
	form.Close()
	body.Write(bytes.Repeat([]byte("x"), 2*maxCheckSpec))
	r := httptest.NewRequest("POST", "/v1/checks", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	s := &Server{State: t.TempDir(), Checks: reviewS2Run(func(context.Context, string, execution.Request) execution.Outcome {
		if body.Len() > 0 {
			t.Errorf("ran with %d unread request bytes; disconnect detection is not established", body.Len())
		}
		return execution.Outcome{Termination: execution.Exited, ExitCode: 0}
	})}
	s.runCheck(httptest.NewRecorder(), r)
}

func TestACancelledUploadStopsItsReads(t *testing.T) {
	var prefix bytes.Buffer
	form := multipart.NewWriter(&prefix)
	spec, _ := form.CreateFormField("spec")
	json.NewEncoder(spec).Encode(CheckSpec{Image: "x", Argv: []string{"true"}, TimeoutSeconds: 1})
	form.CreateFormFile("tree", "tree.tar")
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest("POST", "/v1/checks", io.MultiReader(bytes.NewReader(prefix.Bytes()), pr)).WithContext(ctx)
	r.Header.Set("Content-Type", form.FormDataContentType())
	s := &Server{State: t.TempDir(), Checks: reviewS2Run(func(context.Context, string, execution.Request) execution.Outcome { return execution.Outcome{} })}
	done := make(chan struct{})
	go func() { s.runCheck(httptest.NewRecorder(), r); close(done) }()
	cancel()
	select {
	case <-done:
		return
	case <-time.After(100 * time.Millisecond):
		t.Error("canceled context did not interrupt the tree reader")
	}
	pw.Close()
	<-done
}

func TestEveryEntryCountsAgainstTheExtractionBudget(t *testing.T) {
	var body bytes.Buffer
	tw := tar.NewWriter(&body)
	for _, name := range []string{"a/", "b/"} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0755}); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := extractBounded(&body, root, true, 1024, 1); err == nil {
		t.Fatal("two directories accepted with an entry budget of one")
	}
}

// A request with a part beyond those a check may have is refused before
// anything runs.
func TestACheckRequestWithAnExtraPartIsRefused(t *testing.T) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	spec, _ := form.CreateFormField("spec")
	json.NewEncoder(spec).Encode(CheckSpec{Image: "x", Argv: []string{"true"}, TimeoutSeconds: 1})
	tree, _ := form.CreateFormFile("tree", "tree.tar")
	tar.NewWriter(tree).Close()
	extra, _ := form.CreateFormFile("more", "more.bin")
	extra.Write([]byte("not asked for"))
	form.Close()
	r := httptest.NewRequest("POST", "/v1/checks", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	ran := false
	s := &Server{State: t.TempDir(), Checks: reviewS2Run(func(context.Context, string, execution.Request) execution.Outcome {
		ran = true
		return execution.Outcome{Termination: execution.Exited}
	})}
	w := httptest.NewRecorder()
	s.runCheck(w, r)
	if ran || w.Code != 400 {
		t.Fatalf("a request with an extra part: ran=%v %d %s", ran, w.Code, w.Body.String())
	}
}
