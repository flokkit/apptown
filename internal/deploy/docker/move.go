package docker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/dockerlabel"
)

// Moving a deployment made before its names were by key to them
// (design/subsystems/deployment.md, "Names on a host"): a
// person asks for it, and it runs inside that release's admitted attempt,
// in Boot, after the needs are resolved and the images prepared and before
// any service starts. Its data is copied and checked, never moved: the old
// volumes stay on the host until a person deletes them.
//
// REMOVE-AFTER resource move: this file goes once every deployment on live
// and the demo is moved.

// Move asks a standing boot to move the deployment to its names by key
// first. Switch is the deployment owner's: in one transaction it records
// the deployment as named by key, forgets the containers, networks and
// volumes recorded under its old names, and records made (the copied
// volumes, which the boot then finds and does not record again).
type Move struct {
	Switch func(ctx context.Context, made []deploy.Owned) error
}

// bootMove is a Move for one boot: the names the deployment is moving from.
type bootMove struct {
	Move
	from Names
}

// moveStopWait is how long a stopped container is given to end cleanly: a
// database writes its last checkpoint.
const moveStopWait = "60"

// moveSource is one copy: from a volume the old stack keeps data in, to the
// new name's volume.
type moveSource struct {
	need     string // "" is the app's own data
	from, to string
}

// moveStack runs the move (steps 1–6 of the plan) for the boot t, whose
// names are already the new ones. A failure before the record is switched
// leaves, or puts back, the old stack running; that outcome is known
// (BootError.Known). A failure to put it back is not.
func (h *Host) moveStack(ctx context.Context, t *BootRequest, dataDir string) error {
	dk, m := t.Docker, t.move
	to := Names{prefix: t.names(), stack: t.StackID}
	from := m.from
	t.logf("Moving %s to its names by key: %s, %s", from.App(), to.App(), to.Network())

	// 1. Find the data.
	var sources []moveSource
	needs := make([]string, 0, len(t.own.Needs))
	for n := range t.own.Needs {
		needs = append(needs, n)
	}
	slices.Sort(needs)
	for _, need := range needs {
		path := t.own.Needs[need]
		if path == "" {
			continue
		}
		svc := from.Service(need)
		there, err := dk.Exists(ctx, "inspect", svc)
		if err != nil {
			return movedNothing(fmt.Errorf("the %s service: %w", need, err))
		}
		if there {
			mount, err := DataMountAt(ctx, dk, svc, path)
			if err != nil {
				return movedNothing(err)
			}
			if !mount.Found || mount.Volume == "" {
				return movedNothing(fmt.Errorf("the %s service keeps its data in a host folder or inside its container, not in a volume at %s; move it by hand", need, path))
			}
			sources = append(sources, moveSource{need: need, from: mount.Volume, to: to.NeedVolume(need)})
			continue
		}
		old := from.NeedVolume(need)
		if there, err := dk.Exists(ctx, "volume", "inspect", old); err != nil {
			return movedNothing(err)
		} else if there {
			sources = append(sources, moveSource{need: need, from: old, to: to.NeedVolume(need)})
		}
	}
	if dataDir != "" {
		old := from.DataVolume()
		if there, err := dk.Exists(ctx, "volume", "inspect", old); err != nil {
			return movedNothing(err)
		} else if there {
			sources = append(sources, moveSource{from: old, to: to.DataVolume()})
		}
	}
	containers, err := dk.ListLabelledEither(ctx, []string{"ps", "-aq", "--no-trunc"}, [][2]string{{dockerlabel.Owner, from.Stack()}})
	if err != nil {
		return movedNothing(fmt.Errorf("listing the containers of %s: %w", from.Stack(), err))
	}
	appThere, err := dk.Exists(ctx, "inspect", from.App())
	if err != nil {
		return movedNothing(err)
	}
	for _, s := range sources {
		t.logf("Will copy %s into %s", s.from, s.to)
	}

	// 2. Stop the app, then its stack.
	var stopped []string
	stop := func(c string) error {
		if out, err := dk.Run(ctx, "stop", "-t", moveStopWait, c); err != nil {
			return fmt.Errorf("stopping %s: %v: %s", c, err, strings.TrimSpace(string(out)))
		}
		stopped = append(stopped, c)
		return nil
	}
	var made []deploy.Owned
	undo := func(cause error) error {
		// Put the old stack back as it ran: its services, then its app.
		var failed []error
		for i := len(stopped) - 1; i >= 0; i-- {
			if out, err := dk.Run(context.WithoutCancel(ctx), "start", stopped[i]); err != nil {
				failed = append(failed, fmt.Errorf("starting %s again: %v: %s", stopped[i], err, strings.TrimSpace(string(out))))
			}
		}
		for _, o := range made {
			_ = dk.RemoveVolume(context.WithoutCancel(ctx), o.ID)
		}
		if err := errors.Join(failed...); err != nil {
			return fmt.Errorf("%w; the old stack is stopped and could not be started again: %w", cause, err)
		}
		t.logf("The move did not happen; %s runs under its old names as before", from.App())
		return movedNothing(cause)
	}
	if appThere {
		if err := stop(from.App()); err != nil {
			return undo(err)
		}
	}
	for _, c := range containers {
		if err := stop(c); err != nil {
			return undo(err)
		}
	}

	// 3. Copy and check.
	image := h.chownImage()
	if image == "" {
		image = check.ShImage
	}
	for _, s := range sources {
		// One left by an earlier move that failed: nothing uses it while the
		// record still names the old stack.
		if err := dk.RemoveVolume(ctx, s.to); err != nil {
			return undo(err)
		}
		if out, err := dk.Run(ctx, append(append([]string{"volume", "create", "--label", dockerlabel.Owner + "=" + to.Stack()}, t.own.Info...), s.to)...); err != nil {
			return undo(fmt.Errorf("volume create %s: %v: %s", s.to, err, strings.TrimSpace(string(out))))
		}
		made = append(made, deploy.Owned{Kind: "volume", ID: s.to, Data: true, Need: s.need})
		out, err := dk.Run(ctx, "run", "--rm", "--network", "none", "--entrypoint", "sh",
			"-v", s.from+":/from:ro", "-v", s.to+":/to", image, "-c", copyAndCheck)
		if err != nil {
			return undo(fmt.Errorf("copying %s into %s: %v: %s", s.from, s.to, err, strings.TrimSpace(string(out))))
		}
		t.logf("Copied %s into %s and checked it: %s", s.from, s.to, strings.TrimSpace(string(out)))
	}

	// 5. Switch the record: from here the deployment is named by key. One
	// that may or may not have committed is left as it is, for a person:
	// the old stack stopped, both sets of volumes on the host.
	if err := m.Switch(ctx, made); err != nil {
		return fmt.Errorf("recording the move: %w; %s and its stack are stopped, the copies %s are made, and the old volumes are on the host: look at the deployment's record before starting either", err, from.App(), volumeNames(made))
	}

	// 6. Remove the old containers and network; keep the old volumes.
	var left []string
	for _, c := range append(slices.Clone(containers), from.App()) {
		if c == from.App() && !appThere {
			continue
		}
		if err := dk.Remove(ctx, c); err != nil {
			left = append(left, c)
		}
	}
	if err := dk.RemoveNetwork(ctx, from.Network()); err != nil {
		left = append(left, from.Network())
	}
	var kept []string
	for _, s := range sources {
		kept = append(kept, s.from+" (copied into "+s.to+")")
	}
	if len(kept) > 0 {
		t.logf("Kept on the host, no longer AppTown's: %s. Delete them once you have checked the app.", strings.Join(kept, ", "))
	}
	if len(left) > 0 {
		t.logf("Could not remove, left on the host: %s. Nothing depends on them.", strings.Join(left, ", "))
	}
	return nil
}

// movedNothing is a move that failed with the old stack running as before:
// a known outcome.
func movedNothing(err error) error {
	return &BootError{Phase: PhaseMove, Err: err, Known: true}
}

// copyAndCheck copies /from into /to, the top folder's owner and mode
// too, then checks the copy (sameTree).
const copyAndCheck = `cp -a /from/. /to/ || exit 9
owner=$(stat -c %u:%g /from) || exit 9
mode=$(stat -c %a /from) || exit 9
chown "$owner" /to || exit 9
chmod "$mode" /to || exit 9
` + sameTree

// sameTree compares /from and /to: every entry's path, type, mode and
// owner; a regular file's size and content; a link's target, byte for byte.
// A directory's own size is not compared: it reflects files it once held,
// and a correct copy differs there. Each entry is one line whose fields
// before its path hold no space (a link's target is written in hex, any
// other kind of entry's type as its raw mode), and the path comes last, so
// no two different entries read the same. It prints the copy's entries and
// kilobytes, or fails: exit 3 when the two differ, 4 when a name holds a
// line break (the list is one entry per line, so such an entry is refused
// rather than compared), and 9 when any listing, reading or checksum fails.
// Every read is checked where it is made: a shell does not stop a pipeline
// on a failed command inside it, so nothing it could not read may be
// counted as checked.
const sameTree = `set -u
set -o pipefail
nl='
'
for d in /from /to; do
  odd=$(find "$d" -name "*$nl*") || exit 9
  if [ -n "$odd" ]; then echo "a file name in the data holds a line break; move it by hand"; exit 4; fi
done
list() {
  cd "$1" || exit 9
  find . | LC_ALL=C sort | while IFS= read -r f; do
    if [ -L "$f" ]; then
      m=$(stat -c '%a %u %g' "$f") || exit 9
      t=$(readlink "$f" && echo .) || exit 9
      x=$(printf '%s' "${t%.}" | od -An -tx1 -v | tr -d ' \n') || exit 9
      echo "L $m $x $f"
    elif [ -d "$f" ]; then
      m=$(stat -c '%a %u %g' "$f") || exit 9
      echo "D $m $f"
    elif [ -f "$f" ]; then
      m=$(stat -c '%a %u %g %s' "$f") || exit 9
      h=$(sha256sum < "$f") || exit 9
      echo "F $m ${h%% *} $f"
    else
      m=$(stat -c '%f %a %u %g' "$f") || exit 9
      echo "O $m $f"
    fi
  done
}
a=$(list /from | sha256sum) || exit 9
b=$(list /to | sha256sum) || exit 9
[ "$a" = "$b" ] || { echo "the copy differs from the original"; exit 3; }
n=$(find /to | wc -l) || exit 9
k=$(du -sk /to) || exit 9
echo "$n entries, ${k%%[!0-9]*} KB"
`

func volumeNames(made []deploy.Owned) string {
	var names []string
	for _, o := range made {
		names = append(names, o.ID)
	}
	return strings.Join(names, ", ")
}
