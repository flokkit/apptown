package forge

import (
	"context"
	"net/http"
	"regexp"
	"strings"
)

// The console (design/subsystems/console.md, C1.5): a real terminal on a read-only
// project sandbox. Each session is a docker container created WITHOUT --rm
// whose PID1 is `sleep infinity`; pi's interactive TUI runs under tmux
// *inside* the container (`tmux new -A` via /console/run-pi.sh), with a
// pinned --session-id so the same command creates the conversation on first
// launch and resumes it on every relaunch. forged attaches disposable tmux
// clients (`docker exec -it … tmux` under a pty), keeps a ring buffer for
// reattach, and relays bytes over a websocket to xterm.js.
//
// Why tmux and not `docker attach`: an attach client dying (forged restart,
// crash) sends EOF into the container's stdin, permanently closing pi's
// input. The multiplexer must live with the process; forged's connections
// must be views, not the terminal itself.
//
// Lifecycle ladder (§5b): attached → detached (PTY held, no viewers) →
// stopped (docker stop: filesystem intact, processes dead, zero runtime
// cost) → removed (session ended). Resumes across a stop are never silent:
// a resume notice is appended to the steering file, which pi rereads when
// its process relaunches.
//
// Enforcement is by construction: /work is a read-only mount, the forge CLI
// points at the GET-only listener, and no engine secrets enter the container.
//
// The code is split by concern:
//   console_session.go   the session record, its runtime state, recovery, idle stop
//   console_container.go provisioning, attach/detach, docker and tmux helpers
//   console_steering.go  the prompt, built from sections
//   console_pty.go       the terminal websocket
//   console_record.go    reading pi's session record: messages, hand-back, transcript
//   console_rehearse.go  the boot rehearsal loop and console commits
//   console_api.go       the HTTP handlers

// consoleCarveOut is one mutation the read-only plane lets through. Each is
// gated by something other than network position — the session token
// (invoke, preview, land), a run token's own-change rule (check), or being
// inert by design (complaints, validate). The
// steering's list of what a helper may do is GENERATED from this table, so
// the prompt can never promise a verb the port rejects, or hide one it
// allows.
type consoleCarveOut struct {
	path  *regexp.Regexp
	verb  string // what the helper types
	does  string // plain words for the steering
	scope string // "project": project chats only; "change": change chats only; "": both
}

var consoleCarveOuts = []consoleCarveOut{
	{regexp.MustCompile(`^/api/v1/console/[0-9a-f-]{36}/invoke$`), "forge invoke, forge apply",
		"start work on this project (see \"Starting work\")", "project"},
	{regexp.MustCompile(`^/api/v1/console/[0-9a-f-]{36}/preview$`), "forge preview",
		"boot this change's preview and print a URL you can reach", "change"},
	{regexp.MustCompile(`^/api/v1/console/[0-9a-f-]{36}/land$`), "forge land",
		"start landing this change — only after explicit confirmation", "change"},
	{regexp.MustCompile(`^/api/v1/console/[0-9a-f-]{36}/recheck$`), "forge recheck",
		"after fixing what a landing gate rejected, check the landing again: it catches up with main and reruns the failed gates, or every gate if the change moved (never approves)", "change"},
	{regexp.MustCompile(`^/api/v1/changes/[0-9a-f-]{36}/check$`), "forge check -c <change>",
		"run a change's declared checks against its committed tree", ""},
	{regexp.MustCompile(`^/api/v1/complaints$`), "forge complain",
		"file a complaint about the forge itself", ""},
	{regexp.MustCompile(`^/api/v1/config/validate$`), "forge config validate",
		"check a contract file (writes nothing)", ""},
	{regexp.MustCompile(`^/api/v1/dev-compose$`), "forge dev / forge dev up",
		"render this contract to a local docker-compose for rehearsal (writes nothing)", ""},
}

// consoleAllowedVerbs: the carve-outs a chat of the given kind may use, as
// "`verb` — does" lines for the steering.
func consoleAllowedVerbs(workshop bool) []string {
	var out []string
	for _, c := range consoleCarveOuts {
		if (c.scope == "project" && workshop) || (c.scope == "change" && !workshop) {
			continue
		}
		out = append(out, "`"+c.verb+"` — "+c.does)
	}
	return out
}

// gitFetch is a git fetch's one POST: smart HTTP asks for the pack it wants
// by POSTing to git-upload-pack, which reads and never writes. The git
// endpoint checks the credential itself (githttp.go).
var gitFetch = regexp.MustCompile(`^/git/[^/]+/git-upload-pack$`)

// gitScratchPush is a git push's one POST. It passes here only for the git
// endpoint to refuse every push but one: a step attempt's push of its work
// to its own scratch ref, with its runtime token (githttp.go). A Change
// ticket's push is refused on this listener (onReadOnlyPlane).
var gitScratchPush = regexp.MustCompile(`^/git/[^/]+/git-receive-pack$`)

// readOnlyPlane marks a request that came through GETOnly.
type readOnlyPlane struct{}

// onReadOnlyPlane reports whether r came through the read-only listener.
func onReadOnlyPlane(r *http.Request) bool { return r.Context().Value(readOnlyPlane{}) != nil }

// GETOnly wraps a handler, rejecting every non-GET request plus the PTY
// websocket (a GET, but an input path — sandboxed agents must not reach
// their own or anyone else's terminal). The carve-outs above, a git fetch
// and a step's scratch push are the only POSTs that pass, each doing its
// own credential check.
func GETOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), readOnlyPlane{}, true))
		if r.Method == http.MethodPost {
			// By path, and with no service query: git never sends one with its
			// POSTs, and one naming another service is not let through.
			if (gitFetch.MatchString(r.URL.Path) || gitScratchPush.MatchString(r.URL.Path)) && !r.URL.Query().Has("service") {
				h.ServeHTTP(w, r)
				return
			}
			for _, c := range consoleCarveOuts {
				if c.path.MatchString(r.URL.Path) {
					h.ServeHTTP(w, r)
					return
				}
			}
		}
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) || strings.HasSuffix(r.URL.Path, "/pty") {
			writeErr(w, http.StatusMethodNotAllowed, "read-only console API: not available on this port")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// ReadOnly is the verification startup's door (design/subsystems/postgres-migration.md
// §3.7): GET and HEAD only, no carve-outs — GETOnly's console carve-outs
// (a complaint, a report) are writes, and a verification run must change
// nothing so the old database stays a lossless rollback until acceptance.
func ReadOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeErr(w, http.StatusMethodNotAllowed, "this forge is running in verify-only mode: reads only until the move is accepted")
			return
		}
		h.ServeHTTP(w, r)
	})
}
