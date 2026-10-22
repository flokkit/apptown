package changeadds

import (
	"slices"
	"testing"
)

// Upgrading main and an open change makes sibling rename commits. Their
// merge base still has the old path, including the service the change removes.
func TestAChangeOpenAcrossTheUpgradeShowsAServiceItRemoved(t *testing.T) {
	base := tree{".forge/app.yaml": []byte(contract)}
	head := tree{contractPath: []byte("app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n")}
	got := rowNamed(t, Compare(base.file, head.file, "pre-upgrade base", "upgraded change"), Services)
	if got.Unknown != "" || !slices.Equal(got.Removed, []string{"postgres"}) {
		t.Fatalf("services: %+v; want postgres removed", got)
	}
}
