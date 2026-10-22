package gitlab_test

import (
	"context"
	"slices"
	"testing"

	"github.com/flokkit/apptown/internal/gitlab/gitlabtest"
)

// A merge request's description is replaced whole through the documented
// update endpoint (PUT /projects/:id/merge_requests/:iid with only a
// description), after the one it was opened with.
func TestAMergeRequestDescriptionIsReplacedWhole(t *testing.T) {
	t.Parallel()
	fake := gitlabtest.New(t)
	client, err := fake.Client()
	if err != nil {
		t.Fatal(err)
	}
	mr, err := client.CreateMR(t.Context(), "group/project", "change", "main", "a title", "at open")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.UpdateMRDescription(context.Background(), "group/project", mr.IID, "at landing"); err != nil {
		t.Fatal(err)
	}
	if got := fake.Descriptions("group/project", mr.IID); !slices.Equal(got, []string{"at open", "at landing"}) {
		t.Fatalf("descriptions %q", got)
	}
	fake.RefuseDescriptions(&gitlabtest.Refusal{Status: 403, Message: "403 Forbidden"})
	if err := client.UpdateMRDescription(context.Background(), "group/project", mr.IID, "refused"); err == nil {
		t.Fatal("a refused description update reported success")
	}
	if got := fake.Descriptions("group/project", mr.IID); len(got) != 2 {
		t.Fatalf("a refused update was kept: %q", got)
	}
}
