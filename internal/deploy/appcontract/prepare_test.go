package appcontract_test

import (
	"encoding/json"
	"testing"

	"flokkit.io/forge/internal/deploy/appcontract"
)

func TestPreparedAppIsolatesConsumersAndBoots(t *testing.T) {
	c, err := appcontract.Parse([]byte(`app:
  web:
    image: app:1
    public: true
    command: [serve, "two words"]
    environment: {PUBLIC_ONLY: web, DATABASE_URL: wrong}
    health: {liveness: {exec: [check, original]}}
  worker:
    image: worker:1
    environment: {WORKER_ONLY: worker, DATABASE_URL: wrong}
init:
  - name: migrate
    image: migrate:1
    command: [migrate, original]
    environment: {INIT_ONLY: init, DATABASE_URL: wrong}
`))
	if err != nil {
		t.Fatal(err)
	}
	before := contractJSON(t, c)
	connections := map[string]string{"DATABASE_URL": "resolved"}
	plan := appcontract.Prepare(c, connections)
	another := appcontract.Prepare(c, connections)
	for _, p := range plan.Parts {
		if p.Env["DATABASE_URL"] != "resolved" {
			t.Fatalf("connection precedence: %+v", p)
		}
		if !p.Public && p.Env["PUBLIC_ONLY"] != "" {
			t.Fatal("public environment leaked into worker")
		}
	}
	if plan.Init[0].Env["DATABASE_URL"] != "resolved" || plan.Init[0].Env["PUBLIC_ONLY"] != "" {
		t.Fatal("init environment is not scoped")
	}
	plan.PublicPart().Env["PUBLIC_ONLY"] = "changed"
	plan.PublicPart().Command[1] = "changed"
	plan.PublicPart().Intent.Liveness.Exec[1] = "changed"
	plan.Init[0].Env["INIT_ONLY"] = "changed"
	plan.Init[0].Command[1] = "changed"
	if contractJSON(t, c) != before || another.PublicPart().Env["PUBLIC_ONLY"] != "web" || another.PublicPart().Command[1] != "two words" || another.PublicPart().Intent.Liveness.Exec[1] != "original" || another.Init[0].Command[1] != "original" || connections["DATABASE_URL"] != "resolved" {
		t.Fatal("execution plan mutated the declaration or another boot")
	}
}

func contractJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
