package apptown

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cgi"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec"

	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// The scoped git endpoint (the collaborative git model): apptownd serves
// git-over-HTTP so an agent/console container can PULL the project's main
// branch to stay current — no filesystem mount, and nothing it shouldn't see.
// The shape is enforced by git's own protocol, not by trust:
//
//   - FETCH only. A push (git-receive-pack) is refused outright, except the
//     two narrow pushes handleGitHTTP names (a Change ticket's, a step's
//     scratch push).
//   - ONE project — the token is scoped to it.
//   - MAIN only — uploadpack.hideRefs advertises just the default branch, so
//     no other change's branch is listed OR fetchable (they diverge from main,
//     so their commits are unreachable and unrequestable with the default
//     allow*SHA1InWant off). A helper cannot enumerate or pull other people's
//     in-flight work.
//
// The token IS the capability: a read-only, main-only, one-repo credential the
// AppTown mints and injects into containers. Reachability is free — containers
// already resolve host.docker.internal (the console API base).

// gitTokenProject validates a presented git token (basic-auth password, or a
// Bearer header) and returns its project.
func (a *App) gitTokenProject(r *http.Request) (*project.Project, bool) {
	tok := presentedGitCredential(r)
	if tok == "" {
		return nil, false
	}
	pid, found, err := a.Access.ProjectOfPullToken(r.Context(), tok)
	if !readOK(err) || !found {
		return nil, false
	}
	if p := a.projectByID(r.Context(), pid); p != nil {
		return p, true
	}
	return nil, false
}

// presentedGitCredential: the basic-auth password or Bearer value.
func presentedGitCredential(r *http.Request) string {
	if _, pass, ok := r.BasicAuth(); ok {
		return pass
	}
	if ah := r.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
		return strings.TrimPrefix(ah, "Bearer ")
	}
	return ""
}

// handleGitHTTP serves git smart-HTTP scoped to the credential presented.
// Two credentials open this door (design/subsystems/mcp.md §5):
//
//   - the project's pull token: fetch only, main only (the container case);
//
//   - a Change Git ticket (authz_tickets.go): fetch main and the ticket's
//     Change ref, and push THAT ref from the tip the ticket expected. The
//     hub's pre-receive hook (reconcile.go) enforces ref and tip; git's own
//     config refuses non-fast-forward and deletion. A successful push
//     consumes the ticket and is recorded as the person via their client.
//
//   - a step attempt's runtime token (design/subsystems/guidance-checks.md,
//     "Feedback and evidence"): push only, to that attempt's own scratch ref
//     (scm.ScratchRef), for `apptown check` to check work not yet collected.
//     Every other ref is hidden, and so refused by git itself; the hub's
//     pre-receive hook refuses any other ref and a deletion again. A scratch
//     commit is feedback only, never published and never evidence.
//
// On the read-only listener (GETOnly) the scratch push is the only push.
//
// Bypasses AuthMiddleware (non-/api path) and authenticates itself.
func (a *App) handleGitHTTP(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	gitpath := r.PathValue("gitpath")
	service, ok := gitService(r.Method, gitpath, r.URL.Query())
	if !ok {
		http.Error(w, "git: the request's service query does not match its path", http.StatusBadRequest)
		return
	}
	isPush := service == "git-receive-pack"

	var p *project.Project
	var ticket *identity.Ticket
	var scratch *workflow.StepPush
	if t := a.gitTicketByToken(r.Context(), presentedGitCredential(r)); t != nil {
		if ch, err := a.changeByID(r.Context(), t.ChangeID); err == nil && ch.State == workflow.ChangeInProgress {
			p, ticket = a.projectByID(r.Context(), ch.ProjectID), t
		}
	} else if who, err := inferenceIdentityFor(a).AuthenticateStep(r.Context(), presentedGitCredential(r)); err == nil {
		s, err := a.Work.StepPush(r.Context(), who)
		if err != nil {
			http.Error(w, "git: "+err.Error(), http.StatusForbidden)
			return
		}
		p, scratch = a.projectByID(r.Context(), who.ProjectID), &s
	} else if tp, ok := a.gitTokenProject(r); ok {
		p = tp
	}
	addressID, addressErr := project.IDBySlugIn(r.Context(), a.DB, slug)
	if p == nil || addressErr != nil || p.ID != addressID {
		w.Header().Set("WWW-Authenticate", `Basic realm="AppTown"`)
		http.Error(w, "git: authentication required (a Change ticket from checkout_change, the project's pull token, or a step's token for its scratch ref)", http.StatusUnauthorized)
		return
	}
	// Only the receive-pack discovery and RPC, by method and path: a query
	// never turns another backend operation into a push.
	if scratch != nil && !isPush {
		http.Error(w, "git: a step's token only pushes its work to its scratch ref ("+scratch.Ref+"); it fetches nothing here", http.StatusForbidden)
		return
	}
	if isPush && ticket != nil && onReadOnlyPlane(r) {
		http.Error(w, "git: this port takes no Change ticket pushes; push to AppTown's address checkout_change gave you", http.StatusForbidden)
		return
	}
	if isPush && ticket == nil && scratch == nil {
		http.Error(w, "git: this credential is read-only — pull is allowed, push needs a Change ticket (checkout_change)", http.StatusForbidden)
		return
	}
	hub, err := p.Repository(a.cfg.dataDir)
	if err != nil {
		http.Error(w, "git: repository identity is unavailable", http.StatusInternalServerError)
		return
	}
	if !gitexec.IsRepo(hub) {
		http.NotFound(w, r)
		return
	}
	db := p.DefaultBranch
	if db == "" {
		db = "main"
	}
	root := filepath.Dir(hub)      // .../workspaces
	repoName := filepath.Base(hub) // project-<slug>

	gitBin, err := exec.LookPath("git")
	if err != nil {
		http.Error(w, "git: server has no git binary", http.StatusInternalServerError)
		return
	}
	// Git's old plain-file HTTP reads (info/refs, objects/…, packs) bypass
	// uploadpack.hideRefs and serve any object a caller can name, a sibling
	// change's included; every real client speaks the smart protocol, so the
	// file reads are off for every credential.
	config := "'http.getanyfile=false' 'uploadpack.hideRefs=refs/' 'uploadpack.hideRefs=!refs/heads/" + db + "'"
	env := []string{
		"GIT_PROJECT_ROOT=" + root,
		"GIT_HTTP_EXPORT_ALL=1",
		// Protocol v2's fetch serves any object named by its ID, hidden refs
		// notwithstanding; every credential here is
		// scoped, so the backend speaks protocol v0, which refuses a want
		// for an object no advertised ref reaches. The client's
		// Git-Protocol header is not passed on.
		"GIT_PROTOCOL=",
	}
	var before string
	if ticket != nil {
		// receive-pack ignores uploadpack.hideRefs: its own hideRefs keep a
		// push's discovery to the ticket's branch and the main it builds on
		// plus the holding ref the push lands in.
		config += " 'uploadpack.hideRefs=!" + ticket.Ref + "'" +
			" 'receive.hideRefs=refs/' 'receive.hideRefs=!refs/heads/" + db + "' 'receive.hideRefs=!" + ticket.Ref + "'" +
			" 'receive.hideRefs=!" + scm.IncomingRef(ticket.ID) + "'" +
			" 'http.receivepack=true' 'receive.denyNonFastForwards=true' 'receive.denyDeletes=true'" +
			" 'receive.denyCurrentBranch=ignore' 'receive.updateServerInfo=false'"
		// The push lands in the ticket's holding ref, which must be new. The
		// hook refuses early a push from a moved or unrelated tip; the branch
		// itself moves only through guarded publication below.
		expect := ticket.ExpectedTip
		if expect == "" {
			expect = strings.Repeat("0", 40)
		}
		env = append(env, "REMOTE_USER="+ticket.User, "APPTOWN_PUSH_REF="+scm.IncomingRef(ticket.ID), "APPTOWN_PUSH_BRANCH="+ticket.Ref, "APPTOWN_PUSH_EXPECT="+expect)
		before = scm.Ref(r.Context(), hub, ticket.Ref)
	}
	if scratch != nil {
		// Only the scratch ref and the branches it builds on are advertised,
		// so the push sends only new objects; a hidden ref cannot be pushed
		// at all, and the hook refuses the advertised branches.
		// Reads are off too, should one ever be admitted: no upload-pack, no
		// dumb file serving, and no ref an upload-pack would advertise.
		config = "'http.uploadpack=false' 'http.getanyfile=false' 'uploadpack.hideRefs=refs/'" +
			" 'http.receivepack=true' 'receive.hideRefs=refs/' 'receive.hideRefs=!refs/heads/" + db + "'" +
			" 'receive.hideRefs=!" + scratch.Ref + "'" +
			" 'receive.denyDeletes=true' 'receive.denyNonFastForwards=false'" +
			" 'receive.denyCurrentBranch=ignore' 'receive.updateServerInfo=false'"
		if scratch.Branch != "" && !strings.ContainsAny(scratch.Branch, "' ") {
			config += " 'receive.hideRefs=!refs/heads/" + scratch.Branch + "'"
		}
		env = append(env, "REMOTE_USER="+scratch.Actor, "APPTOWN_SCRATCH_REF="+scratch.Ref)
	}
	env = append(env, "GIT_CONFIG_PARAMETERS="+config)
	// The backend starts with an environment of its own (no HOME), so only
	// the machine's system config could still reach it.
	env = append(env, gitexec.RepositoryConfigOnly...)
	h := &cgi.Handler{
		Path: gitBin,
		Args: []string{"http-backend"},
		Dir:  root,
		Root: "/git/" + slug,
		Env:  env,
	}
	// cgi strips Root from the path to compute PATH_INFO. Point PATH_INFO at
	// project-<slug> under GIT_PROJECT_ROOT.
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/git/" + slug + "/" + repoName + "/" + gitpath
	release, ok := withKnownLength(w, r2, filepath.Join(a.cfg.dataDir, "spool"), a.cfg.seeding.BundleLimit())
	if !ok {
		return
	}
	defer release()
	h.ServeHTTP(w, r2)

	if ticket != nil && isPush && r.Method == http.MethodPost {
		// Not cancelled with the request: the push has landed, and a client
		// hanging up must not leave it half-published.
		if err := contributionsFor(a).PublishPush(context.WithoutCancel(r.Context()), ticket); err != nil {
			log.Printf("git ticket %d: the push was not published: %v", ticket.ID, err)
		}
		if after := scm.Ref(r.Context(), hub, ticket.Ref); after != "" && after != before {
			spendPushedTicket(r.Context(), a.Access, a.DB, audit.At(a.cfg.clock.Now()), p.ID, ticket, before, after)
		}
	}
}

// withKnownLength gives r a body of known length, within limit, the largest
// Git data a client may upload (-import-bundle-max-bytes). Go's CGI host
// refuses a chunked body, and git sends any push or fetch request larger
// than its http.postBuffer (1 MiB by default) chunked, so such a body is
// first copied to a file in spool. release removes it; ok
// false means the answer has been written.
func withKnownLength(w http.ResponseWriter, r *http.Request, spool string, limit int64) (release func(), ok bool) {
	tooLarge := func() (func(), bool) {
		http.Error(w, fmt.Sprintf("git: this push is larger than AppTown accepts (%d bytes; the operator sets -import-bundle-max-bytes)", limit), http.StatusRequestEntityTooLarge)
		return nil, false
	}
	if r.ContentLength > limit {
		return tooLarge()
	}
	if r.ContentLength >= 0 && len(r.TransferEncoding) == 0 {
		return func() {}, true
	}
	if err := os.MkdirAll(spool, 0o700); err != nil {
		http.Error(w, "git: could not hold the request: "+err.Error(), http.StatusInternalServerError)
		return nil, false
	}
	f, err := os.CreateTemp(spool, "git-body-*")
	if err != nil {
		http.Error(w, "git: could not hold the request: "+err.Error(), http.StatusInternalServerError)
		return nil, false
	}
	release = func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}
	n, err := io.Copy(f, io.LimitReader(r.Body, limit+1))
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		release()
		http.Error(w, "git: the request could not be received: "+err.Error(), http.StatusBadRequest)
		return nil, false
	}
	if n > limit {
		release()
		return tooLarge()
	}
	r.Body, r.ContentLength, r.TransferEncoding = f, n, nil
	return release, true
}

// spendPushedTicket marks a landed push's ticket spent, with its ledger
// line. The push has landed, so both are written though the client that
// pushed hangs up: a ticket left unspent would push again.
func spendPushedTicket(ctx context.Context, ids *identity.Service, db *database.DB, at audit.At, projectID ids.ID, ticket *identity.Ticket, before, after string) {
	ctx = context.WithoutCancel(ctx)
	if consumed, err := ids.ConsumeTicket(ctx, ticket.ID); err != nil {
		log.Printf("git ticket %d: the push landed but the ticket could not be marked consumed: %v", ticket.ID, err)
	} else if consumed {
		at.BestEffort(ctx, db, ticket.User+" via git ticket", projectID, "change.revision-pushed", fmt.Sprintf("change %d", ticket.ChangeID),
			map[string]any{"ref": ticket.Ref, "from": before, "to": after, "ticketId": ticket.ID, "delegationId": ticket.DelegationID})
	} else {
		at.BestEffort(ctx, db, ticket.User+" via git ticket", projectID, "change.ticket-already-spent", fmt.Sprintf("change %d", ticket.ChangeID),
			map[string]any{"ticketId": ticket.ID, "to": after})
	}
}

// gitService is the smart-HTTP service a request runs, read as git's backend
// reads it: from the method and path. The service query names it only on
// discovery (GET or HEAD of info/refs). A service query anywhere else that
// names another service than the path is a conflict (ok false), refused
// before any credential decides on it. "" is neither smart service (dumb
// file reads, dumb discovery).
func gitService(method, gitpath string, q url.Values) (service string, ok bool) {
	asked := q.Get("service")
	switch {
	case (method == http.MethodGet || method == http.MethodHead) && gitpath == "info/refs":
		if asked == "git-upload-pack" || asked == "git-receive-pack" {
			return asked, true
		}
		return "", asked == ""
	case method == http.MethodPost && (gitpath == "git-upload-pack" || gitpath == "git-receive-pack"):
		return gitpath, asked == "" || asked == gitpath
	}
	return "", asked == ""
}
