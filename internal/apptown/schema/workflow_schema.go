package schema

import (
	_ "embed"
	"encoding/json"

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
	var expand func(any)
	expand = func(value any) {
		switch v := value.(type) {
		case map[string]any:
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
	raw, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	return raw
}
