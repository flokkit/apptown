package docker

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestImageRetentionDeletesOnlyRecordedExpiredAndOwnedBuildTags(t *testing.T) {
	t.Parallel()
	refs := map[string]string{"registry.test/app@sha256:old": "sha256:old", "registry.test/app@sha256:kept": "sha256:kept"}
	var deleted []string
	fake := &fakeClient{reply: func(req Command) ([]byte, error) {
		args := req.Args
		switch {
		case slices.Equal(args, []string{"image", "ls", "--no-trunc", "--format", "{{json .}}"}):
			var out strings.Builder
			for _, img := range []struct{ ID, Repository, Tag string }{{"sha256:old", "apptown-preview-build-build-old-web", "latest"}, {"sha256:kept", "apptown-preview-build-build-kept-web", "latest"}, {"sha256:human", "human", "latest"}} {
				raw, _ := json.Marshal(img)
				fmt.Fprintln(&out, string(raw))
			}
			return []byte(out.String()), nil
		case args[0] == "image" && strings.Contains(args[3], "RepoTags"):
			for _, ref := range args[4:] {
				id := refs[ref]
				fmt.Fprintf(req.Stdout, "[]\t[%q]\t%q\n", ref, id)
			}
			return nil, nil
		case args[0] == "image":
			return []byte("sha256:kept"), nil
		case args[0] == "rmi":
			deleted = append(deleted, args[1])
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected Docker request %v", args)
		}
	}}
	got, left, err := (Scope{Client: fake}).TrimImages(t.Context(), []string{"registry.test/app@sha256:old"}, []string{"registry.test/app@sha256:kept"}, "apptown-")
	if err != nil || len(left) != 0 || len(got) != 2 {
		t.Fatalf("trim: %v %v %v", got, left, err)
	}
	for _, ref := range deleted {
		if strings.Contains(ref, "kept") || strings.Contains(ref, "human") {
			t.Fatalf("kept/unrecorded image deleted: %s", ref)
		}
	}
}
