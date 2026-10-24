// Package docs is AppTown's own documentation, embedded in apptownd: the
// guides, the integration pages and the API description. It describes the
// binary, so it moves with it; the stock in stock/ is read from the update
// files a release carries instead.
package docs

import "embed"

//go:embed integrations/*.md guides/*.md guides/index.yaml api/openapi.json
var FS embed.FS
