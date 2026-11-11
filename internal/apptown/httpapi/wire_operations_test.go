package httpapi

import (
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/wiregen"
)

// The operation page's response. OperationView encodes itself (the
// operation's own encoding with the project added); operationViewWire
// describes that encoding, the operation's fields flattened beside the
// project.
func init() {
	registerWireMapped(OperationView{}, "OperationView")
	registerWire(wiregen.Named{Name: "OperationView", Value: operationViewWire{}})
}

// OperationFields is the operation without its MarshalJSON, so the
// generator flattens its fields as the encoding does.
type OperationFields operations.Operation

type operationViewWire struct {
	OperationFields
	Project *OperationProject `json:"project,omitempty"`
}
