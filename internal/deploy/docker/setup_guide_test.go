package docker

import (
	"os"
	"strings"
	"testing"
)

// The Docker host setup guide names, in its "Who owns what" section, every kind
// of thing this driver's take-down deletes, in the words a take-down's result
// uses for it (teardownKinds, the list the take-down itself runs through). A kind
// added there fails here until the guide says who owns it. It does not prove
// every creation site uses one of these kinds.
func TestSetupGuideNamesEveryOwnedKind(t *testing.T) {
	const guide = "../../../stock/docs/guides/docker-host-target-setup.md"
	body, err := os.ReadFile(guide)
	if err != nil {
		t.Fatal(err)
	}
	_, section, found := strings.Cut(string(body), "\n## Who owns what\n")
	if !found {
		t.Fatalf("%s has no \"Who owns what\" section", guide)
	}
	section, _, _ = strings.Cut(section, "\n## ")
	for _, kind := range teardownKinds {
		if !strings.Contains(section, "`"+kind+"`") {
			t.Errorf("%s, \"Who owns what\": the take-down deletes %q, which the section does not name", guide, kind)
		}
	}
}
