package preview

import (
	"context"
	"errors"
	"flokkit.io/forge/internal/dockerlabel"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/preview/internal/previewsql"
)

// A preview stops eight hours after anyone last looked at it, and a
// change's preview keeps its build images and its snapshot for thirty days
// after the last look (design/subsystems/container-lifecycle.md). A look is
// a request to the preview's address through AppTown (vhost.go) or a boot
// of it.
const (
	idleStop  = 8 * time.Hour
	keepBuild = 30 * 24 * time.Hour
	// lookEvery is how often one preview's look is written at most: every
	// request to a page's assets is a look, and a minute is fine enough
	// for an eight-hour rule.
	lookEvery = time.Minute
)

// looks is when this AppTown last wrote each preview's look.
type looks struct {
	mu      sync.Mutex
	written map[string]time.Time
}

// Viewed records a look at the preview by this name (its app container's)
// now, on AppTown's clock, at most once a minute per preview. A look is
// never moved backwards. A write that fails is logged, and the next look
// tries again.
func (s *Service) Viewed(ctx context.Context, name string) {
	if s == nil {
		return
	}
	now := s.now()
	s.looks.mu.Lock()
	if last, ok := s.looks.written[name]; ok && !now.Before(last) && now.Sub(last) < lookEvery {
		s.looks.mu.Unlock()
		return
	}
	if s.looks.written == nil {
		s.looks.written = map[string]time.Time{}
	}
	s.looks.written[name] = now
	s.looks.mu.Unlock()
	// The look happened though its request ends now.
	if err := previewsql.New(s.db.SQL()).RecordPreviewView(context.WithoutCancel(ctx), previewsql.RecordPreviewViewParams{Preview: name, ViewedAt: now}); err != nil {
		log.Printf("recording a look at %s: %v", name, err)
		s.looks.mu.Lock()
		if s.looks.written[name].Equal(now) {
			delete(s.looks.written, name)
		}
		s.looks.mu.Unlock()
	}
}

// lastLooks is when each preview was last looked at.
func (s *Service) lastLooks(ctx context.Context) (map[string]time.Time, error) {
	rows, err := previewsql.New(s.db.SQL()).PreviewViews(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]time.Time, len(rows))
	for _, r := range rows {
		out[r.Preview] = r.ViewedAt
	}
	return out, nil
}

// Tend stops each preview nobody has looked at for eight hours, wherever
// it is, and removes what a change's preview kept (its build images and
// its snapshot) once thirty days have passed since its last look, with the
// look. Build images and snapshots of a change's preview with no look,
// that is not present, go too: nothing from before AppTown recorded
// looks counts as looked at. Looks that cannot be read, or a place
// previews run that cannot be asked, stop nothing and remove nothing this
// pass. logOnly names what it would do and does nothing.
func (s *Service) Tend(ctx context.Context, logOnly bool) {
	looks, err := s.lastLooks(ctx)
	if err != nil {
		log.Printf("tending previews: reading their looks: %v", err)
		return
	}
	present, err := s.presentPreviews(ctx)
	if err != nil {
		log.Printf("tending previews: listing them: %v", err)
		return
	}
	now := s.now()
	for name, at := range present {
		if seen, ok := looks[name]; !ok || now.Sub(seen) > idleStop {
			s.stopIdle(ctx, name, at, looks, logOnly)
		}
	}
	for name, seen := range looks {
		if _, here := present[name]; !here && now.Sub(seen) > keepBuild {
			s.expireKept(ctx, name, now.Add(-keepBuild), logOnly)
		}
	}
	s.removeUnlooked(ctx, looks, present, logOnly)
}

// place is where a present preview is: on the worker, or on AppTown's
// own Docker with the stack its app names.
type place struct {
	worker bool
	stack  string
}

// presentPreviews are the previews present, running or not (an app that
// exited is still there, with its stack): every preview app on AppTown's
// own Docker when it has one, and every preview on the worker when there is
// one. Both can hold previews at once: a release recorded before bundles
// boots its look here beside a worker.
func (s *Service) presentPreviews(ctx context.Context) (map[string]place, error) {
	out := map[string]place{}
	if dk := s.hosts.Local(); !dk.Absent() {
		lines, err := dk.List(ctx, "ps", "-a", "--filter", "label="+dockerlabel.Class+"=preview", "--format", `{{.Names}}`+"\t"+`{{.Label "`+docker.StackLabel+`"}}`)
		if err != nil {
			return nil, err
		}
		for _, line := range lines {
			name, stack, _ := strings.Cut(line, "\t")
			if docker.IsPreviewName(name) {
				out[name] = place{stack: stack}
			}
		}
	}
	if s.PreviewsOnWorker() {
		names, err := s.worker.List(ctx)
		if err != nil {
			return nil, &UnavailableError{Cause: err}
		}
		for _, name := range names {
			out[name] = place{worker: true}
		}
	}
	return out, nil
}

// stopIdle stops one preview nobody has looked at for eight hours, unless a
// boot holds it now: a change's where changes' previews run as the
// landing's completion discards it, any other by forgetting its record and
// removing it where it is. Its build images stay.
func (s *Service) stopIdle(ctx context.Context, name string, at place, looks map[string]time.Time, logOnly bool) {
	why := "no look recorded"
	if seen, ok := looks[name]; ok {
		why = "last looked at " + seen.UTC().Format(time.RFC3339)
	}
	if logOnly {
		log.Printf("previews: WOULD stop %s (%s)", name, why)
		return
	}
	unlock, busy := s.tryHoldPreview(name)
	if busy {
		return // booting now: that boot is a look
	}
	defer unlock()
	// The look read earlier is a candidate only: a boot that finished
	// since, or a request, looked at it again.
	row, err := previewsql.New(s.db.SQL()).PreviewView(ctx, name)
	if found, err := database.Lookup(err); err != nil || found && s.now().Sub(row.ViewedAt) <= idleStop {
		return
	}
	if id, ok := docker.ParsePreviewChangeID(name); ok && at.worker == s.PreviewsOnWorker() {
		err = s.discardChangePreview(ctx, id)
	} else {
		s.discardDecided(name)
		if at.worker {
			err = s.removeOnWorker(ctx, name)
		} else {
			err = docker.RemovePreview(context.WithoutCancel(ctx), s.hosts.Local(), name, at.stack)
		}
	}
	if err != nil {
		log.Printf("previews: stopping %s (%s): %v", name, why, err)
		return
	}
	log.Printf("previews: stopped %s (%s)", name, why)
}

// expireKept removes what one preview kept, then its look: a change's
// build images and its snapshot. The look read earlier is a candidate
// only: under the preview's lock its look is read again and the preview
// looked for, and only a look still older than before, of a preview not
// present, goes on. A boot records its look before it takes that lock, so
// a reopen is either seen here or begins after the removal and builds
// again. A removal that fails, or an image a container still uses, keeps
// the look for the next pass.
func (s *Service) expireKept(ctx context.Context, name string, before time.Time, logOnly bool) {
	if logOnly {
		log.Printf("previews: WOULD remove what %s kept (no look since %s)", name, before.UTC().Format(time.RFC3339))
		return
	}
	unlock, busy := s.tryHoldPreview(name)
	if busy {
		return
	}
	defer unlock()
	if !s.stillUnlooked(ctx, name, before) {
		return
	}
	if id, ok := docker.ParsePreviewChangeID(name); ok {
		if err := s.removeKept(ctx, id); err != nil {
			log.Printf("previews: removing what %s kept: %v", name, err)
			return
		}
	}
	if _, err := previewsql.New(s.db.SQL()).ForgetPreviewViewBefore(ctx, previewsql.ForgetPreviewViewBeforeParams{Preview: name, Before: before}); err != nil {
		log.Printf("previews: forgetting the look at %s: %v", name, err)
		return
	}
	log.Printf("previews: removed what %s kept (no look since %s)", name, before.UTC().Format(time.RFC3339))
}

// stillUnlooked says, under the preview's lock, whether its look is still
// older than before (or there is none, for a zero before) and the preview
// is not present. Anything that cannot be read says no.
func (s *Service) stillUnlooked(ctx context.Context, name string, before time.Time) bool {
	row, err := previewsql.New(s.db.SQL()).PreviewView(ctx, name)
	found, err := database.Lookup(err)
	switch {
	case err != nil:
		return false
	case found && (before.IsZero() || !row.ViewedAt.Before(before)):
		return false
	case !found && !before.IsZero():
		return false // forgotten since: nothing of it is this pass's
	}
	present, err := s.presentPreviews(ctx)
	if err != nil {
		return false
	}
	_, here := present[name]
	return !here
}

// errKeptInUse is a kept image a container still uses: it stays, and so
// does the look.
var errKeptInUse = errors.New("a container still uses its build image")

// removeKept removes a change's preview's build images wherever previews
// run (the worker, and AppTown's own Docker), and its snapshot folder,
// which is always on AppTown.
func (s *Service) removeKept(ctx context.Context, changeID ids.ID) error {
	name := docker.PreviewChangeName(changeID)
	if s.PreviewsOnWorker() {
		if err := s.worker.RemoveImages(ctx, name); err != nil {
			s.claimAgainIfLost(ctx, err)
			return &UnavailableError{Cause: err}
		}
	}
	if dk := s.hosts.Local(); !dk.Absent() {
		inUse, err := dk.RemoveBuildImagesOf(ctx, name)
		if err != nil {
			return err
		}
		if inUse {
			return errKeptInUse
		}
	}
	return os.RemoveAll(s.changeSnapshots(changeID))
}

// removeUnlooked removes the snapshot folders, and the build images on the
// forge's own Docker, of each change's preview with no look that is not
// present: what was on this AppTown before it recorded looks. Each is
// checked again under the preview's lock.
func (s *Service) removeUnlooked(ctx context.Context, looks map[string]time.Time, present map[string]place, logOnly bool) {
	owners := map[ids.ID]bool{}
	if dk := s.hosts.Local(); !dk.Absent() {
		repos, err := dk.List(ctx, "image", "ls", "--format", "{{.Repository}}", "--filter", "reference="+docker.ChangeBuildImages)
		if err != nil {
			log.Printf("previews: listing kept build images: %v", err)
			return
		}
		for _, repo := range repos {
			if id, ok := docker.ChangeOfBuildImage(repo); ok {
				owners[id] = true
			}
		}
	}
	entries, err := os.ReadDir(filepath.Join(s.dataDir, "workspaces"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("previews: listing kept snapshots: %v", err)
		return
	}
	for _, e := range entries {
		if rest, ok := strings.CutPrefix(e.Name(), "asof-change-"); ok {
			if id, err := ids.Parse(rest); err == nil && !id.IsZero() {
				owners[id] = true
			}
		}
	}
	for id := range owners {
		name := docker.PreviewChangeName(id)
		if _, ok := looks[name]; ok {
			continue
		}
		if _, ok := present[name]; ok {
			continue
		}
		if logOnly {
			log.Printf("previews: WOULD remove what %s kept (no look recorded)", name)
			continue
		}
		s.removeUnlookedOne(ctx, id)
	}
}

func (s *Service) removeUnlookedOne(ctx context.Context, changeID ids.ID) {
	name := docker.PreviewChangeName(changeID)
	unlock, busy := s.tryHoldPreview(name)
	if busy {
		return
	}
	defer unlock()
	if !s.stillUnlooked(ctx, name, time.Time{}) {
		return
	}
	switch err := s.removeKept(ctx, changeID); {
	case err == nil:
		log.Printf("previews: removed what %s kept (no look recorded)", name)
	case !errors.Is(err, errKeptInUse):
		log.Printf("previews: removing what %s kept: %v", name, err)
	}
}

// RemovePreviewForGood removes everything of one preview for a cleanup
// that must know it worked (a deleted project's): on the worker its whole
// stack and the build images it kept, and its look. Nothing is kept for a
// reopen. Without a worker its stack and images on this machine are the
// caller's, as AppTown's own Docker is.
func (s *Service) RemovePreviewForGood(ctx context.Context, name string) error {
	if s.PreviewsOnWorker() {
		if err := s.removeOnWorker(ctx, name); err != nil {
			return err
		}
		if err := s.worker.RemoveImages(ctx, name); err != nil {
			s.claimAgainIfLost(ctx, err)
			return &UnavailableError{Cause: err}
		}
	}
	if _, err := previewsql.New(s.db.SQL()).ForgetPreviewView(ctx, name); err != nil {
		return fmt.Errorf("forgetting the look at %s: %w", name, err)
	}
	return nil
}

// changeSnapshots is where a change's preview keeps its snapshots of
// history (deploy.PreviewSource).
func (s *Service) changeSnapshots(changeID ids.ID) string {
	return filepath.Join(s.dataDir, "workspaces", fmt.Sprintf("asof-change-%d", changeID))
}
