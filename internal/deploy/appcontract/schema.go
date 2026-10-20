package appcontract

import _ "embed"

// JSONSchema is the JSON Schema for .apptown/app.yaml, served to models and
// editors. It describes the structure the parser accepts; rules a schema cannot
// express (exactly one public part, needs in the catalog) stay in Parse. A test
// holds its properties to the parser's own document types.
//
//go:embed schema.json
var JSONSchema []byte
