package schema

import _ "embed"

// WorkflowJSONSchema is the JSON Schema for a workflow as a team saves it,
// served to editors and agents. It describes the structure ParseWorkflow
// accepts; rules a schema cannot express stay in the parser. A test holds
// its keys and step kinds to the parser's own.
//
//go:embed workflow.schema.json
var WorkflowJSONSchema []byte
