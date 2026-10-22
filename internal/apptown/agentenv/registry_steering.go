package agentenv

import (
	"fmt"
	"sort"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/integration"
)

func RegistrySteering(bound map[string]integration.BoundRegistry) string {
	if len(bound) == 0 {
		return ""
	}
	formats := make([]string, 0, len(bound))
	for f := range bound {
		formats = append(formats, f)
	}
	sort.Strings(formats)
	render := func(layer string) string {
		var b strings.Builder
		for _, f := range formats {
			ad := registryAdapterFor(f)
			if ad == nil || ad.Layer != layer {
				continue
			}
			fmt.Fprintf(&b, "- %s: %s\n", ad.Title, ad.Mechanism)
		}
		return b.String()
	}
	var b strings.Builder
	b.WriteString("\n## Ambient registry environment (this instance, generated)\n")
	if pkgs := render("packages"); pkgs != "" {
		b.WriteString("\n### Package registries (universal — apply to any build)\n\n")
		b.WriteString("Already configured in your environment; do not edit, do not hardcode:\n\n")
		b.WriteString(pkgs)
	}
	if tc := render("toolchain"); tc != "" {
		b.WriteString("\n### Toolchain mirrors (mise/rustup supply line)\n\n")
		b.WriteString("Used when tools install via mise.toml declarations:\n\n")
		b.WriteString(tc)
	}
	b.WriteString("\nRun long-lived commands (servers, watchers, `cargo run` on a service)\n")
	b.WriteString("with an explicit timeout or in the background — a foreground server\n")
	b.WriteString("blocks your step until the platform timeout kills the container.\n")
	b.WriteString("\nEcosystems not listed go direct. Nested environments you create\n")
	b.WriteString("(Dockerfiles, CI) inherit NONE of this — propagate explicitly per\n")
	b.WriteString("the registries guidance.\n")
	return b.String()
}
