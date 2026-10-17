package docker

import (
	"context"
	"errors"
	"testing"
)

// Only the daemon's own "no such object" answer means already removed; a
// missing socket also says "no such" and must stay a failure.
func TestRemovalCountsOnlyTheDaemonsAnswerAsAlreadyGone(t *testing.T) {
	t.Parallel()
	c := &fakeClient{out: "error during connect: Delete http://%2Fvar%2Frun%2Fdocker.sock/v1.47/containers/preview?force=1: dial unix /var/run/docker.sock: connect: no such file or directory", err: errors.New("exit status 1")}
	if err := (Scope{Client: c}).Remove(context.Background(), "preview"); err == nil {
		t.Fatal("unreachable daemon was reported as successful removal")
	}
	gone := &fakeClient{out: "Error response from daemon: No such container: preview", err: errors.New("exit status 1")}
	if err := (Scope{Client: gone}).Remove(context.Background(), "preview"); err != nil {
		t.Fatalf("an already removed container: %v", err)
	}
}
