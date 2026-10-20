package workflowexec

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/durable"
	"flokkit.io/forge/internal/execution"
)

// A write/search-only directory permits the blob's rename but refuses the
// open required for its directory fsync. No mock or production seam is used.
// Accepting that visible blob must still require the durability barrier.
func TestReviewSP01RequestDoesNotBypassFailedBlobDirectorySync(t *testing.T) {
	input, err := execution.NewInput("/apptown/in/prompt", []byte("admitted input"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	runner := &TaskRunner{Requests: t.TempDir()}
	operation := "review-sp01"
	dir := filepath.Join(runner.Requests, operation)
	blobs := filepath.Join(dir, blobsDir)
	if err := os.MkdirAll(blobs, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blobs, 0o700) })
	if err := os.Chmod(blobs, 0o300); err != nil {
		t.Fatal(err)
	}
	if f, err := os.Open(blobs); err == nil {
		f.Close()
		t.Skip("requires an unprivileged user: this process can read a write/search-only directory")
	} else if !errors.Is(err, os.ErrPermission) {
		t.Fatal(err)
	}

	// Seed exactly the state left by a blob write whose rename succeeded
	// but whose directory could not be synced.
	if _, err := durable.Write(blobs, input.Blob+".tar", input.BlobTar(), 0o600, false); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("blob write error = %v, want directory permission failure", err)
	}
	if _, err := os.Stat(filepath.Join(blobs, input.Blob+".tar")); err != nil {
		t.Fatalf("blob's rename did not precede the failure: %v", err)
	}
	request := admittedRequest{Task: execution.TaskSpec{Inputs: []execution.Input{input}}}
	_, err = runner.keepRequest(operation, request)
	_, requestErr := os.Stat(filepath.Join(dir, requestFile))
	if err == nil || requestErr == nil {
		t.Fatalf("accepted a blob whose directory cannot be synced: error=%v, request.json exists=%v", err, requestErr == nil)
	}
}
