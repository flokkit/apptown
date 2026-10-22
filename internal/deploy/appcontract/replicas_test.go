package appcontract_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// Explicit single-instance intent has exactly the existing execution meaning;
// accepting the field must not change the prepared container or its resources.
func TestSingleReplicaDeclarationPreservesExistingExecution(t *testing.T) {
	const base = "app:\n  web:\n    image: example/web:1\n    public: true\n    resources: {memory: 64M}\n"
	want, err := appcontract.Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"1", "null"} {
		t.Run(value, func(t *testing.T) {
			got, err := appcontract.Parse([]byte(base + "    replicas: " + value + "\n"))
			if err != nil {
				t.Fatalf("single-instance declaration must be accepted: %v", err)
			}
			if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(appcontract.Prepare(got, nil), appcontract.Prepare(want, nil)) {
				t.Fatalf("single-instance declaration changed the contract or prepared execution: %+v", got)
			}
		})
	}
}

// All app parts share the boundary. No supported source kind or private worker
// may silently turn a multi-instance request into one running container.
func TestMultipleReplicasAreRefusedForEveryAppPart(t *testing.T) {
	for _, source := range []string{"build: .", "image: example/web:1", "static: public", "static: {build: ., output: dist}"} {
		t.Run(source, func(t *testing.T) {
			_, err := appcontract.Parse([]byte("app:\n  web:\n    " + source + "\n    public: true\n    replicas: 2\n"))
			assertReplicaRefusal(t, err, "web", "not supported")
		})
	}
	_, err := appcontract.Parse([]byte("app:\n  web: {image: example/web:1, public: true}\n  worker: {image: example/worker:1, replicas: 2}\n"))
	assertReplicaRefusal(t, err, "worker", "not supported")
}

func TestReplicasRequirePositiveIntegerCounts(t *testing.T) {
	for _, value := range []string{"0", "-1", `"1"`, "1.0", "1.5", "true", "[]", "{}"} {
		t.Run(value, func(t *testing.T) {
			_, err := appcontract.Parse([]byte("app:\n  web: {image: example/web:1, public: true, replicas: " + value + "}\n"))
			assertReplicaRefusal(t, err, "web", "positive integer")
		})
	}
}

func assertReplicaRefusal(t *testing.T, err error, part, reason string) {
	t.Helper()
	if err == nil {
		t.Fatal("replicas request was accepted instead of refused")
	}
	for _, word := range []string{"replicas", `"` + part + `"`, reason} {
		if !strings.Contains(err.Error(), word) {
			t.Fatalf("refusal must identify the field, part and reason (%q): %v", word, err)
		}
	}
	if reason == "not supported" && (!strings.Contains(err.Error(), "2") || !strings.Contains(err.Error(), "1")) {
		t.Fatalf("unsupported count must name requested 2 and supported 1: %v", err)
	}
}

func TestReplicasDoNotIntroduceProfileOrInitScaling(t *testing.T) {
	const base = "app:\n  web: {image: example/web:1, public: true}\n"
	for _, extra := range []string{
		"profiles:\n  production:\n    web: {replicas: 1}\n",
		"init:\n  - {name: migrate, image: example/migrate:1, replicas: 1}\n",
	} {
		if _, err := appcontract.Parse([]byte(base + extra)); err == nil || !strings.Contains(err.Error(), "replicas") {
			t.Fatalf("replicas must remain an app-part field, not profile/init scaling: %v", err)
		}
	}
}

func TestReplicasSchemaDescribesDefaultAndUnsupportedCounts(t *testing.T) {
	var schema struct {
		Properties struct {
			App struct {
				Part struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"additionalProperties"`
			} `json:"app"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(appcontract.JSONSchema, &schema); err != nil {
		t.Fatal(err)
	}
	var replicas struct {
		Type        []string `json:"type"`
		Minimum     int      `json:"minimum"`
		Default     int      `json:"default"`
		Description string   `json:"description"`
	}
	if err := json.Unmarshal(schema.Properties.App.Part.Properties["replicas"], &replicas); err != nil {
		t.Fatalf("published app-part schema must describe replicas: %v", err)
	}
	if !reflect.DeepEqual(replicas.Type, []string{"integer", "null"}) || replicas.Minimum != 1 || replicas.Default != 1 || !strings.Contains(replicas.Description, "not supported") {
		t.Fatalf("schema must describe optional positive counts, default 1 and current unsupported replication: %+v", replicas)
	}
}
