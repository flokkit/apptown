package preview

import (
	"context"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"net"
	"net/http"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/deploy/docker"
)

// Served is a running preview of a change or a run, opened for
// probing, wherever it runs: which container it is and what it serves, its
// front door (through AppTown's preview proxy), and its naked door (this
// machine's loopback, with no front-door signal: the app's own port here,
// or a relay to it on the worker). Close ends the relay.
type Served struct {
	Name, ContainerID, Incarnation string
	FrontDoor, Naked               string
	// Source and Recorded are AppTown's record of what this incarnation
	// was booted from and with: the commit ("" for a change's folder) and
	// its public prepared values, its contract among them. Recorded is nil
	// when AppTown holds no record of the incarnation the container
	// says it serves.
	Source   string
	Recorded *docker.PublicPrepared

	service *Service
	relay   *http.Server
}

// OpenPreview opens the running preview a label names (change-N, run-N) in
// projectID, whose routes name its front door. ok
// is false when none runs; an error is a place previews run that could not
// be asked, which says nothing about the preview.
func (s *Service) OpenPreview(ctx context.Context, projectID ids.ID, label string) (p *Served, ok bool, err error) {
	name := docker.PreviewLabelName(label)
	p = &Served{Name: name, service: s}
	defer func() {
		if ok {
			if r, found := s.servedRecordOf(name); found && r.Incarnation == p.Incarnation {
				public := r.Public
				p.Source, p.Recorded = r.Source, &public
			}
		}
	}()
	if s.PreviewsOnWorker() {
		st, err := s.PreviewOnWorker(ctx, name)
		if err != nil || !st.Running {
			return nil, false, err
		}
		p.ContainerID, p.Incarnation = st.ContainerID, st.Incarnation
		p.FrontDoor = s.env.RoutesFor(ctx, projectID).PreviewURL(label, 0)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, false, fmt.Errorf("opening this machine's door to the preview: %w", err)
		}
		p.relay = &http.Server{Handler: s.worker.Forward(name, ""), ReadHeaderTimeout: 30 * time.Second}
		go func() { _ = p.relay.Serve(ln) }()
		p.Naked = "http://" + ln.Addr().String()
		return p, true, nil
	}
	id, running, incarnation, err := s.localPreview(ctx, name)
	if err != nil || !running {
		return nil, false, err
	}
	port, err := docker.ContainerHostPort(ctx, s.hosts.Local(), name, 0)
	if err != nil || port == 0 {
		return nil, false, nil
	}
	p.ContainerID, p.Incarnation = id, incarnation
	p.FrontDoor, p.Naked = s.env.RoutesFor(ctx, projectID).PreviewURL(label, port), s.hosts.Local().ProbeURL(port)
	return p, true, nil
}

// localPreview reads a preview container on this machine: its ID, whether
// it runs, and the incarnation it serves (docker.Inspect). A container that
// is not there is not running.
func (s *Service) localPreview(ctx context.Context, name string) (id string, running bool, incarnation string, err error) {
	rt, err := docker.Inspect(ctx, s.hosts.Local(), name)
	if errors.Is(err, docker.ErrContainerAbsent) {
		return "", false, "", nil
	}
	if err != nil {
		return "", false, "", err
	}
	return rt.ID, rt.Running, rt.Labels[dockerlabel.Incarnation], nil
}

// Same says whether the preview is still the container that was opened,
// running. An error is a place that could not be asked.
func (p *Served) Same(ctx context.Context) (bool, error) {
	if p.service.PreviewsOnWorker() {
		st, err := p.service.PreviewOnWorker(ctx, p.Name)
		return err == nil && st.Running && st.ContainerID == p.ContainerID, err
	}
	id, running, _, err := p.service.localPreview(ctx, p.Name)
	return err == nil && running && id == p.ContainerID, err
}

// Browser is what the preview's container was started with for its page,
// with the container's ID: docker.ErrNoBrowserValues when that cannot be
// read.
func (p *Served) Browser(ctx context.Context) (docker.InstalledBrowser, error) {
	if p.service.PreviewsOnWorker() {
		st, err := p.service.PreviewOnWorker(ctx, p.Name)
		if err != nil {
			return docker.InstalledBrowser{}, err
		}
		if st.BrowserValues == nil {
			return docker.InstalledBrowser{ID: st.ContainerID}, docker.ErrNoBrowserValues
		}
		return docker.InstalledBrowser{ID: st.ContainerID, Values: st.BrowserValues}, nil
	}
	return docker.InspectBrowserValues(ctx, p.service.hosts.Local(), p.Name)
}

// Close ends the relay to a preview on the worker.
func (p *Served) Close() {
	if p != nil && p.relay != nil {
		_ = p.relay.Close()
	}
}
