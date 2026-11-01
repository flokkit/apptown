package deployment

import (
	"encoding/json"
	"testing"
)

func TestKindWords(t *testing.T) {
	for _, k := range []Kind{Standing, preview} {
		stored, err := k.Value()
		var read Kind
		if err != nil || read.Scan(stored) != nil || read != k {
			t.Fatalf("%s did not round-trip: %v %v", k, read, err)
		}
	}
	var unknown Kind
	if err := unknown.Scan("environment"); err == nil {
		t.Fatal("an unknown kind was accepted")
	}
	var zero Kind
	if _, err := zero.Value(); err == nil {
		t.Fatal("the zero kind was stored")
	}
	body, err := json.Marshal(struct{ Kind Kind }{preview})
	if err != nil || string(body) != `{"Kind":"preview"}` {
		t.Fatalf("json: %s %v", body, err)
	}
	if !(&Deployment{Kind: Standing}).Standing() || (&Deployment{Kind: preview}).Standing() {
		t.Fatal("Standing does not tell the kinds apart")
	}
}
