package worker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"flokkit.io/forge/internal/version"
)

// The worker protocol says which forges a worker serves
// (design/operations/workers.md, "Versions"). A forge sends the number it
// speaks on every request; a worker serves a range of them. A change that
// breaks the older side raises the forge's number and the worker's
// ProtocolMax, and the worker keeps the previous number for one release, so
// a worker installed before its forge (the demo's order) still serves the
// forge it replaces.
const (
	ProtocolHeader = "X-Forge-Worker-Protocol"
	// Protocol is what this build's forge speaks. 2: a task's observation
	// says whether Docker killed it at its memory limit (OOMKilled), and a
	// forge that settles scripts by it must not hear a worker that cannot
	// say, which would read every such kill as an ordinary exit.
	Protocol = 2
	// ProtocolMin and ProtocolMax are what this build's worker serves: 1
	// for one release, so a worker installed first serves its old forge.
	ProtocolMin = 1
	ProtocolMax = 2
)

// VersionReport is GET /v1/version.
type VersionReport struct {
	Version     string `json:"version"`
	ProtocolMin int    `json:"protocolMin"`
	ProtocolMax int    `json:"protocolMax"`
}

// served refuses a request whose forge speaks a protocol outside this
// worker's range, by name. A request without the header comes from a forge
// built before protocols were stated, and is served.
func served(next http.Handler) http.Handler { return servedRange(ProtocolMin, ProtocolMax, next) }

// servedRange is served for a worker serving min to max.
func servedRange(min, max int, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get(ProtocolHeader); h != "" {
			n, err := strconv.Atoi(h)
			if err != nil || n < min || n > max {
				http.Error(w, fmt.Sprintf("this worker (%s) serves forge protocols %d to %d; AppTown speaks %q: install AppTown and its worker from the same release",
					version.String(), min, max, h), http.StatusUpgradeRequired)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(VersionReport{Version: version.String(), ProtocolMin: ProtocolMin, ProtocolMax: ProtocolMax})
}

// SpeakProtocol wraps a forge's transport to a worker so every request
// states the protocol this forge speaks.
func SpeakProtocol(rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return protocolTransport{rt}
}

type protocolTransport struct{ next http.RoundTripper }

func (t protocolTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set(ProtocolHeader, strconv.Itoa(Protocol))
	return t.next.RoundTrip(r)
}
