package deployment

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

// HostReaders runs one sequential reader per active Docker host. Foreground
// operations use Hosts independently and never write the stored reading.
type HostReaders struct {
	Snapshots *HostSnapshots
	Targets   *Targets
	Hosts     *Hosts
	Providers *Providers
	Inventory *Inventory
}

const hostReadInterval = 20 * time.Second

// Run owns the loops until cancellation, including cancellation of host I/O.
// Target changes wake reconciliation; unreachable targets keep their loop.
func (r HostReaders) Run(ctx context.Context) {
	type loop struct {
		cancel context.CancelFunc
		done   chan struct{}
	}
	loops := map[ids.ID]loop{}
	var wg sync.WaitGroup
	defer func() {
		for _, l := range loops {
			l.cancel()
		}
		wg.Wait()
	}()
	reconcile := func() {
		targets, err := r.Targets.activeDocker(ctx)
		if err != nil {
			log.Printf("host readers: listing targets: %v", err)
			return
		}
		active := map[ids.ID]bool{}
		for _, t := range targets {
			if !r.Hosts.readsDockerHost(t) {
				continue
			}
			active[t.ID] = true
			if _, ok := loops[t.ID]; ok {
				continue
			}
			life, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			loops[t.ID] = loop{cancel, done}
			wg.Add(1)
			go func(id ids.ID) {
				defer wg.Done()
				defer close(done)
				for life.Err() == nil {
					if err := r.lap(life, id); err != nil && life.Err() == nil {
						log.Printf("host reading %s: %v", id, err)
					}
					timer := time.NewTimer(hostReadInterval)
					select {
					case <-life.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
				}
			}(t.ID)
		}
		for id, l := range loops {
			if !active[id] {
				l.cancel()
				<-l.done
				delete(loops, id)
			}
		}
	}
	reconcile()
	// Targets.changed wakes only its own Targets instance; reconcile other instances too.
	ticker := time.NewTicker(hostReadInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.Targets.changed:
			reconcile()
		case <-ticker.C:
			reconcile()
		}
	}
}

func (r HostReaders) lap(ctx context.Context, id ids.ID) error {
	t, err := r.Targets.ByID(ctx, id)
	if err != nil {
		return err
	}
	if t.Status == TargetRetired {
		return nil
	}
	dk, err := r.Hosts.DockerScope(ctx, t)
	if err != nil {
		return err
	}
	dk.Now = r.Snapshots.now
	// The loop is already serialized. Its one command must not occupy one of
	// the three foreground permits, but still uses the shared session cap.
	if client, ok := dk.Client.(sshDocker); ok {
		client.reader = true
		dk.Client = client
	}
	refs, err := r.Providers.recordedImages(ctx, t)
	if err != nil {
		return err
	}
	var also []string
	if t.Origin == OriginBuiltIn && r.Providers.databaseContainer != "" {
		also = []string{r.Providers.databaseContainer}
	}
	return r.Snapshots.collect(ctx, t, dk, docker.HostQuery{Host: true, Containers: true, Resources: true, Images: true, ImageRefs: refs}, also, r.Inventory)
}
