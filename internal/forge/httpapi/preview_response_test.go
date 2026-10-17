package httpapi

import (
	"testing"

	"flokkit.io/forge/internal/forge/preview"
)

func TestPreviewCommitDisplay(t *testing.T) {
	for _, revision := range []string{"", "abc", "1234567890"} {
		out := previewResponse(preview.Result{URL: "https://preview", Revision: revision}, false)
		want := revision
		if len(want) > 7 {
			want = want[:7]
		}
		if out.URL != "https://preview" || out.Commit != want {
			t.Fatalf("preview %q: %+v", revision, out)
		}
	}
}

// The page shows, rather than opens, an address the owner says answers on
// the forge host only.
func TestPreviewResponseCarriesTheHostOnlyMark(t *testing.T) {
	if out := previewResponse(preview.Result{URL: "http://forge.example:40001"}, true); !out.HostOnly {
		t.Fatalf("host-only preview: %+v", out)
	}
	if out := previewResponse(preview.Result{URL: "https://change-00000000-0007-7000-8000-000000000000.preview.example"}, false); out.HostOnly {
		t.Fatalf("preview at its domain: %+v", out)
	}
}
