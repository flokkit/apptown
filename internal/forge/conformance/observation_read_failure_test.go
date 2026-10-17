package conformance

import (
	"testing"

	"flokkit.io/forge/internal/ids"
)

func TestCurrentObservationDoesNotHideFailedEvidenceRead(t *testing.T) {
	t.Parallel()
	db, projectID := reviewFixture(t)
	service := newServiceWithAuthority(db, testAuthority{}, unavailableHeadSource{testCheckSource{}})
	view, err := service.View(caller("t"), ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	observations := view.ByProject["reviewed"]
	if len(observations) != 1 || observations[0].ProjectID != projectID || observations[0].Record != nil || observations[0].State != NotScanned {
		t.Fatalf("absence: %+v", observations)
	}
	t.Cleanup(db.FailNextStatement("FROM conformance_verdicts"))
	if vs, err := service.View(caller("t"), ids.Nil); err == nil || vs != nil {
		t.Fatalf("failed lookup became absence: %+v %v", vs, err)
	}
}
