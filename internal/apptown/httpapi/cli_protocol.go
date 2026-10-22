package httpapi

import (
	"net/http"
	"strconv"

	"github.com/flokkit/apptown/internal/version"
)

// CLIProtocol names this AppTown's version on every response and refuses a
// CLI that states a protocol outside the range this AppTown accepts, with a
// sentence the CLI prints. A request that states none (a browser, an MCP
// client, curl, or a CLI built before protocols were stated) is served.
func CLIProtocol(next http.Handler) http.Handler {
	v := version.String()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(version.Header, v)
		if h := r.Header.Get(version.CLIProtocolHeader); h != "" {
			n, err := strconv.Atoi(h)
			if err != nil || n < version.CLIProtocolMin || n > version.CLIProtocolMax {
				WriteError(w, http.StatusUpgradeRequired, "this AppTown (%s) accepts CLI protocols %d to %d; this CLI speaks %q: use the CLI from the same release as AppTown",
					v, version.CLIProtocolMin, version.CLIProtocolMax, h)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
