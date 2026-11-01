package maintenance

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/dockerlabel"

	"github.com/flokkit/apptown/internal/database"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/pi"
)

// The container reaper (design/subsystems/container-lifecycle.md): a periodic backstop to
// event-driven reaping. Event reaping leaks on crashes/missed events; this
// sweep catches the leaks. It reaps EPHEMERAL AppTown containers whose owner is
// idle or gone, and is structurally incapable of touching persistent env
// containers or unlabelled infra (it only considers forge.class-labelled ones,
// and env is explicitly skipped).

const reapIdleTTL = 2 * time.Hour // a container idle/stale past 2h is reapable
// reapByAge decides whether a bounded-life ephemeral container is stale. env is
// persistent (never), console is decided by idleness elsewhere, a preview by
// Preview (the last look at it, preview.Service.Tend), unknown classes are
// left alone. This is the safety-critical invariant, isolated for testing.
func reapByAge(class string, created, now time.Time) bool {
	switch class {
	case "service", "step", "dind":
		return !created.IsZero() && now.Sub(created) > reapIdleTTL
	default:
		return false // env / console / preview / unknown → not by age
	}
}

// reapContainers sweeps once. Returns the names reaped (or, in log-only mode,
// the names it would have reaped).
func (r *Reaper) Containers(ctx context.Context, logOnly bool) []string {
	if r.docker.Absent() {
		return nil // no local Docker, nothing of it to reap
	}
	out, err := r.docker.Run(ctx, "ps", "-a",
		"--filter", "label="+dockerlabel.Class,
		"--format", "{{.Names}}\t{{.Label \""+dockerlabel.Class+"\"}}\t{{.Label \""+dockerlabel.Created+"\"}}\t{{.Label \""+dockerlabel.Owner+"\"}}\t{{.ID}}")
	if err != nil {
		return nil
	}

	// Console idleness comes from the in-memory registry, not the label: a
	// console is reapable only when NO client is attached and it has been idle
	// past the TTL. Reaping a detached console is safe — it resumes its pi
	// session on reopen.
	now := r.now()
	consoleReapable := map[string]*console.Session{}
	for _, s := range r.sessions.List(ctx, console.Match{}) {
		if s.Idle(now, reapIdleTTL) {
			consoleReapable[pi.ContainerName(s.ID)] = s
		}
	}

	var reaped []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\t", 5)
		name := f[0]
		class := ""
		if len(f) > 1 {
			class = f[1]
		}
		created := ""
		if len(f) > 2 {
			created = f[2]
		}

		// Consoles are reaped via the registry pass below (authoritative, and it
		// catches legacy unlabelled consoles too), not by this label scan.
		if class == "console" {
			continue
		}
		t, _ := time.Parse(time.RFC3339, created)
		if !reapByAge(class, t, now) {
			continue
		}

		// Unknown ownership is not evidence that an execution has stopped.
		if len(f) < 5 || f[4] == "" || !executionOwnerFinished(ctx, r.db, f[3]) {
			continue
		}
		// A retry can reuse a name. Remove the observed container, never its replacement.
		identity := f[4]
		reaped = append(reaped, name)
		if logOnly {
			log.Printf("reaper: WOULD reap %s (class=%s, created=%s)", name, class, created)
			continue
		}
		log.Printf("reaper: reaping %s (class=%s)", name, class)
		r.docker.RemoveWithVolumes(ctx, identity)
	}

	reaped = append(reaped, r.executionNetworks(ctx, logOnly)...)

	// Console pass: registry-driven, so it covers both labelled and legacy
	// unlabelled console containers. Idle + unattached ⇒ reap the CONTAINER
	// only — the session STAYS registered, container-less, so reopening the
	// chat resumes it (ensureAttached reprovisions on the next message).
	// Deleting sessions from the registry here broke the documented resume:
	// a reopened chat minted a fresh empty session instead.
	for name, s := range consoleReapable {
		if !s.Idle(r.now(), reapIdleTTL) {
			continue
		}
		st, err := r.runtime.Status(ctx, s.ID)
		if err != nil {
			log.Printf("reaper: inspecting console %s: %v", name, err)
			continue
		}
		// A chat already reaped stays registered and idle: with nothing of
		// it on the host there is nothing to do, and saying "reaping" on
		// every pass claimed a removal that never happened.
		if !st.Exists && !st.SidecarExists && !st.ScriptExists {
			continue
		}
		reaped = append(reaped, name)
		if logOnly {
			log.Printf("reaper: WOULD reap %s and its private Docker (session stays resumable)", name)
			continue
		}
		// The container and the sidecar go; the session stays registered,
		// so reopening the chat resumes it.
		log.Printf("reaper: reaping %s and its private Docker (session stays resumable)", name)
		if err := r.sessions.Evict(ctx, s.ID); err != nil {
			log.Printf("reaper: removing console %s: %v", name, err)
		}
	}

	return reaped
}

// executionNetworks removes the networks of executions that have finished:
// a step's services network and its private daemon's, labelled at creation
// with the class and owner their containers carry. Their own teardown
// removes them; this catches what a stopped AppTown or a failed removal left,
// after the pass above has taken their containers. Each one Docker holds
// counts against its address pools. A network without those labels (a
// standing deployment's, Docker's own, one made before the labels) is never
// selected, nor one whose owner is unknown or unreadable; a network
// something is still on is refused by Docker. It is removed by the ID
// listed, never a replacement made since.
func (r *Reaper) executionNetworks(ctx context.Context, logOnly bool) []string {
	var reaped []string
	for _, line := range r.docker.Lines(ctx, "network", "ls", "--filter", "label="+dockerlabel.Class,
		"--format", "{{.ID}}\t{{.Name}}\t{{.Label \""+dockerlabel.Class+"\"}}\t{{.Label \""+dockerlabel.Owner+"\"}}") {
		f := strings.SplitN(line, "\t", 4)
		if len(f) < 4 || f[0] == "" || f[3] == "" {
			continue
		}
		id, name, class, owner := f[0], f[1], f[2], f[3]
		if class != "step" && class != "service" && class != "dind" {
			continue
		}
		if !executionOwnerFinished(ctx, r.db, owner) {
			continue
		}
		reaped = append(reaped, name)
		if logOnly {
			log.Printf("reaper: WOULD remove network %s (class=%s)", name, class)
			continue
		}
		log.Printf("reaper: removing network %s (class=%s)", name, class)
		r.docker.RemoveNetwork(ctx, id)
	}
	return reaped
}

// executionOwnerFinished interprets the existing owner labels emitted by step
// launchers. A failed read or an unrecognised owner retains resources;
// a periodic backstop must not interrupt live work based on age alone.
func executionOwnerFinished(ctx context.Context, db database.Queryer, owner string) bool {
	id := leadingID(owner)
	preview := false
	if id.IsZero() {
		if strings.HasPrefix(owner, "pv") {
			id = leadingID(strings.TrimPrefix(owner, "pv"))
			preview = true
		} else {
			// A step's containers are named execution.StepContainer; its
			// private daemon apptown-dind-step-<id>.
			for _, kind := range []string{"step-", "dind-step-"} {
				if prefix := string(docker.Current) + kind; strings.HasPrefix(owner, prefix) {
					id = leadingID(strings.TrimPrefix(owner, prefix))
					break
				}
			}
		}
	}
	if id.IsZero() {
		return false
	}
	step, err := workflow.ReadStepIn(ctx, db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	if err != nil || step == nil {
		return false
	}
	if step.Status == workflow.StatusPending || step.Status == workflow.StatusRunning {
		return false
	}
	if preview {
		run, err := workflow.ReadRunIn(ctx, db, step.RunID)
		return err == nil && run != nil && run.Status != workflow.RunRunning
	}
	return true
}
