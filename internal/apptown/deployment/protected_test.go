package deployment

import (
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

func TestProtectionNeedsReleaseRightsAndPersonalMeaning(t *testing.T) {
	t.Parallel()
	s, w, dep := classified(t, "public", "production")
	in := ProtectedSettings{Was: true, Meaning: unprotectMeaning(dep.DisplayLabel)}
	for _, protected := range []bool{false, true} {
		w.deny = access.CapReleasePromote
		in.Protected = protected
		if _, err := s.SetProtected(t.Context(), dep.ProjectID, dep.Name, in); fault.KindOf(err) != fault.Forbidden {
			t.Fatalf("changed without release rights: %v", err)
		}
	}
	w.deny, in.Protected = "", false
	for _, via := range []string{access.ViaTokenAgent, access.ViaOAuth} {
		w.via = via
		if _, err := s.SetProtected(t.Context(), dep.ProjectID, dep.Name, in); fault.KindOf(err) != fault.Forbidden {
			t.Fatalf("non-assenting %s changed protection: %v", via, err)
		}
	}
	w.via = "header"

	w.agent = true
	if _, err := s.SetProtected(t.Context(), dep.ProjectID, dep.Name, in); fault.KindOf(err) != fault.Forbidden {
		t.Fatalf("unverified caller changed protection: %v", err)
	}
	w.agent = false
	in.Meaning = "I agree"
	if _, err := s.SetProtected(t.Context(), dep.ProjectID, dep.Name, in); fault.KindOf(err) != fault.Unprocessable {
		t.Fatalf("wrong meaning accepted: %v", err)
	}
	in.Meaning = unprotectMeaning(dep.DisplayLabel)
	got, err := s.SetProtected(t.Context(), dep.ProjectID, dep.Name, in)
	if err != nil || got.Protected || got.ProtectedReason != "" {
		t.Fatalf("not switched off: %+v %v", got, err)
	}

	kept, err := s.Deployments.EnsureStanding(t.Context(), dep.ProjectID, dep.Name, dep.TargetID, dep.releaseAttributes())
	if err != nil || kept.Protected || kept.ProtectedReason != "" {
		t.Fatalf("upsert reset protection: %+v %v", kept, err)
	}
	r := receipts(t, s.db, "deployment.unprotected")
	if len(r) != 1 || !strings.Contains(string(r[0].Payload), in.Meaning) {
		t.Fatalf("missing exact assent: %+v", r)
	}
	if _, err := s.SetProtected(t.Context(), dep.ProjectID, dep.Name, in); fault.KindOf(err) != fault.Conflict {
		t.Fatalf("stale edit accepted: %v", err)
	}
	got, err = s.SetProtected(t.Context(), dep.ProjectID, dep.Name, ProtectedSettings{Protected: true, Was: false})
	if err != nil || !got.Protected || got.ProtectedReason != "" {
		t.Fatalf("not switched on: %+v %v", got, err)
	}
}

func TestRestoredPlacementStartsProtectedForProductionData(t *testing.T) {
	t.Parallel()
	s, _, in, target := configurationFixture(t)
	for _, data := range []string{"production", "synthetic"} {
		dep, err := s.Deployments.PlaceRestoredCopy(t.Context(), RestoredPlacement{ProjectID: in.ProjectID, TargetID: target.ID, Name: "copy-" + data, DataClass: data, BackupID: ids.New(time.Now()), At: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		stored, err := s.Deployments.ByID(t.Context(), dep.ID)
		if err != nil || stored.Protected != (data == "production") {
			t.Fatalf("restored copy protection: %+v %v", stored, err)
		}
		if data == "production" && stored.ProtectedReason != "production data" {
			t.Fatalf("starting reason: %q", stored.ProtectedReason)
		}
	}
}
