// Package stock embeds the vendor workflow library shipped with AppTown.
// Synced into the instance library at startup (state-model §7.1: vendor
// workflows version with the release).
package stock

import "embed"

// all:seeds — plain "seeds" silently drops dotfiles, which cost us the
// .apptown/app.yaml runtime contracts seeds are supposed to carry.
//
//go:embed *.yaml toolchains/*.toml dependencies/*.yaml all:guidance docs/integrations/*.md docs/guides/*.md docs/guides/index.yaml docs/api/openapi.json all:seeds
var FS embed.FS
