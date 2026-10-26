package deploy

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

type collisionDelivery map[string]map[string]string

func (d collisionDelivery) EnvFor(consumer string) map[string]string { return d[consumer] }

func TestNeedSecretCollisionSelectionPreservesScopeAndValueBytes(t *testing.T) {
	c := &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true, Env: map[string]string{"DATABASE_URL": "placeholder"}}, {Name: "worker"}}, Init: []appcontract.InitContainer{{Name: "migrate"}}}
	names := []string{"PGHOST", "DATABASE_URL", "DATABASE_URL"}
	delivery := collisionDelivery{"worker": {"DATABASE_URL": "opaque$\\value"}, "unused": {"PGHOST": "unused-secret"}, "web": {"SESSION_KEY": "opaque$\\value"}}
	plan := appcontract.Prepare(c, map[string]string{"DATABASE_URL": "actual$\\need"})
	if err := RefuseNeedSecretCollisions(plan, names, delivery); err == nil || !strings.Contains(err.Error(), "worker") {
		t.Fatalf("full consumer collision: %v", err)
	}
	selected := plan.PublicOnly()
	if err := RefuseNeedSecretCollisions(selected, names, delivery); err != nil {
		t.Fatalf("omitted worker scope refused: %v", err)
	}
	if selected.PublicPart().Env["DATABASE_URL"] != "actual$\\need" || delivery["web"]["SESSION_KEY"] != "opaque$\\value" {
		t.Fatal("validation changed supplied bytes or placeholder precedence")
	}
	delivery["init-migrate"] = map[string]string{"PGHOST": ""}
	if err := RefuseNeedSecretCollisions(selected, names, delivery); err == nil || !strings.Contains(err.Error(), "init-migrate") || !strings.Contains(err.Error(), "PGHOST") {
		t.Fatalf("public-only init collision ignored: %v", err)
	}
}
