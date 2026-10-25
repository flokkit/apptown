package httpapi

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
)

// A failed lookup cannot be reported as absence or change existing offers.
func TestTargetOfferCommandsSeparateAbsenceFromReadFailure(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			w := newTargetHTTPWorld(t)
			target, err := w.deployment.Targets.Put(t.Context(), &deployment.Target{Name: "shared", Kind: deployment.KindDockerHost, Origin: deployment.OriginAttached, Status: deployment.TargetReady})
			if err != nil {
				t.Fatal(err)
			}
			if err := w.deployment.Targets.Offer(t.Context(), target, "*", deployment.OfferCaps{MaxStanding: 1}, "bls"); err != nil {
				t.Fatal(err)
			}
			before, err := w.deployment.Targets.Offers(t.Context(), target.ID)
			if err != nil || len(before) != 1 {
				t.Fatalf("seed offers: %v %v", before, err)
			}
			handler := w.targets.OfferTarget
			if method == http.MethodDelete {
				handler = w.targets.RevokeOffer
			}
			call := func(name string) (int, map[string]any) {
				return targetCall(t, handler, method, "/api/v1/targets/"+name+"/offers", `{"group":"*","maxStanding":2}`, map[string]string{"name": name, "id": before[0].ID.String()})
			}
			if code, out := call("absent"); code != http.StatusNotFound || !strings.Contains(fmt.Sprint(out["error"]), `no target named "absent"`) {
				t.Fatalf("absence: %d %v", code, out)
			}
			restore := w.db.FailNextStatement("-- name: TargetByName")
			code, out := call(target.Name)
			restore()
			if code != http.StatusInternalServerError {
				t.Errorf("failed target read: %d %v", code, out)
			}
			after, err := w.deployment.Targets.Offers(t.Context(), target.ID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("lookup failure changed offers: before=%v after=%v err=%v", before, after, err)
			}
			if code, out := call(target.Name); code != http.StatusOK {
				t.Fatalf("successful control: %d %v", code, out)
			}
			after, err = w.deployment.Targets.Offers(t.Context(), target.ID)
			if err != nil {
				t.Fatal(err)
			}
			if method == http.MethodDelete {
				if len(after) != 0 {
					t.Fatalf("successful withdrawal retained offers: %v", after)
				}
			} else if len(after) != 1 || after[0].Caps.MaxStanding != 2 {
				t.Fatalf("successful offer did not update caps: %v", after)
			}
		})
	}
}
