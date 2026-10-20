// Package appcontract owns Forge's native .apptown/app.yaml declaration, parsing,
// validation, and preparation of independent execution inputs.
//
// Parse accepts source bytes; callers own file and repository lookup. Prepare
// accepts already-resolved service connections; callers own provisioning,
// image selection, scoped secret delivery, health rendering, and execution.
// This package has no runtime collaborators or persistence dependencies.
// App-part replicas may explicitly declare 1 (also the omitted/null default);
// larger counts are refused during parsing, before any runtime effects.
package appcontract
