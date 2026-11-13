package schema

import (
	_ "embed"
	"encoding/json"
	"slices"

	"github.com/flokkit/apptown/internal/fix"
)

// WorkflowJSONSchema is the JSON Schema for a workflow as a team saves it,
// served to editors and agents. It describes the structure ParseWorkflow
// accepts; rules a schema cannot express stay in the parser. A test holds
// its keys and step kinds to the parser's own.
var WorkflowJSONSchema = workflowJSONSchema()

//go:embed workflow.schema.json
var workflowSchemaTemplate []byte

func workflowJSONSchema() []byte {
	var document any
	if err := json.Unmarshal(workflowSchemaTemplate, &document); err != nil {
		panic(err)
	}
	root := document.(map[string]any)
	constraints := root["$defs"].(map[string]any)["stepOptionValues"].(map[string]any)
	kinds := []string{}
	for kind, step := range StepTypes {
		if step.Authored {
			kinds = append(kinds, kind)
		}
	}
	slices.Sort(kinds)
	var expand func(any)
	expand = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if v["$comment"] == "step-types" {
				delete(v, "$comment")
				v["enum"] = kinds
			}
			if v["$comment"] == "step-options" {
				delete(v, "$comment")
				conditions := []any{}
				for _, kind := range kinds {
					properties := map[string]any{}
					values, _ := constraints[kind].(map[string]any)
					for _, option := range StepTypes[kind].Options {
						value := values[option]
						if value == nil {
							value = map[string]any{}
						}
						properties[option] = value
					}
					conditions = append(conditions, map[string]any{
						"if":   map[string]any{"properties": map[string]any{"uses": map[string]any{"const": kind}}, "required": []string{"uses"}},
						"then": map[string]any{"properties": map[string]any{"with": map[string]any{"type": "object", "additionalProperties": false, "properties": properties}}},
					})
				}
				v["allOf"] = conditions
			}
			if v["$comment"] == "fix-kind" {
				delete(v, "$comment")
				v["enum"] = fix.Names()
			}
			for _, child := range v {
				expand(child)
			}
		case []any:
			for _, child := range v {
				expand(child)
			}
		}
	}
	expand(document)
	delete(root["$defs"].(map[string]any), "stepOptionValues")
	raw, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	return raw
}
