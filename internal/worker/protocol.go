package worker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"flokkit.io/forge/internal/version"
)

// The worker protocol says which AppTown installations a worker serves
// (design/operations/workers.md, "Versions"). An AppTown sends the number it
// speaks on every request; a worker serves a range of them. A change that
// breaks the older side raises AppTown's number and the worker's
// ProtocolMax, and the worker keeps the previous number for one release, so
// a worker installed before its AppTown (the demo's order) still serves the
// AppTown it replaces.
const (
	ProtocolHeader = "X-AppTown-Worker-Protocol"
	// Protocol is what this build's AppTown speaks. 2: a task's observation
	// says whether Docker killed it at its memory limit (OOMKilled), and a
	// AppTown that settles scripts by it must not hear a worker that cannot
	// say, which would read every such kill as an ordinary exit.
	Protocol = 2
	// ProtocolMin and ProtocolMax are what this build's worker serves: 1
	// for one release, so a worker installed first serves its old AppTown.
	ProtocolMin = 1
	ProtocolMax = 2
)

// VersionReport is GET /v1/version.
type VersionReport struct {
	Version     string `json:"version"`
	ProtocolMin int    `json:"protocolMin"`
	ProtocolMax int    `json:"protocolMax"`
}

// served refuses a request whose AppTown speaks a protocol outside this
// worker's range, by name. A request without the header comes from an AppTown
// built before protocols were stated, and is served.
func served(next http.Handler) http.Handler { return servedRange(ProtocolMin, ProtocolMax, next) }

// servedRange is served for a worker serving min to max.
func servedRange(min, max int, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get(ProtocolHeader); h != "" {
			n, err := strconv.Atoi(h)
			if err != nil || n < min || n > max {
				http.Error(w, fmt.Sprintf("this worker (%s) serves AppTown protocols %d to %d; AppTown speaks %q: install AppTown and its worker from the same release",
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

// SpeakProtocol wraps an AppTown's transport to a worker so every request
// states the protocol this AppTown speaks.
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
