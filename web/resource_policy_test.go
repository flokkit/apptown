package web

import "testing"

func TestImagePolicyNamesOnlyTheTourOrigin(t *testing.T) {
	t.Parallel()
	origins := ImageOrigins()
	if len(origins) != 1 || origins[0] != "https://apptown.dev" {
		t.Fatalf("unexpected remote image policy: %v", origins)
	}
}
