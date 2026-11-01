package deployment

import (
	"context"
	"fmt"
	"log"

	"github.com/flokkit/apptown/internal/apptown/project"

	"github.com/flokkit/apptown/internal/deploy/docker"
)

type Routing struct {
	PublicHost, Scheme                                   string
	PreviewScheme, PreviewDomain, MainScheme, MainDomain string
	// EvalScheme and EvalDomain are where eval copies are served, and only
	// they (evals.go).
	EvalScheme, EvalDomain string
}

func (r Routing) publicURL(port int) string {
	host := r.PublicHost
	if host == "" {
		host = "localhost"
	}
	return fmt.Sprintf("http://%s:%d", host, port)
}

// PortAddressesHostOnly says whether an address at a port this AppTown's own
// Docker publishes (PublicURL) is out of reach of the people it is shown to.
// Those ports are published on the host's loopback only
// (docker.Scope.PublishAddress), so such an address answers on AppTown
// host and nowhere else; a public host says people reach AppTown from
// elsewhere.
func (r Routing) PortAddressesHostOnly() bool { return r.PublicHost != "" }

func (r Routing) PreviewURL(label string, port int) string {
	if r.PreviewDomain != "" {
		return fmt.Sprintf("%s://%s.%s", r.PreviewScheme, label, r.PreviewDomain)
	}
	return r.publicURL(port)
}
func (r Routing) DeploymentURL(servingLabel string, port int) string {
	if r.MainDomain != "" {
		return fmt.Sprintf("%s://%s.%s", r.MainScheme, servingLabel, r.MainDomain)
	}
	return r.publicURL(port)
}

// reach is where people reach a deployment, and whether that address
// answers on the AppTown host only (Routing.PortAddressesHostOnly): shown, it
// is a place to go on that host, not a link for anyone elsewhere.
type reach struct {
	URL      string
	HostOnly bool
}

// URL is where people reach a deployment now (reachedAt).
func (s *Service) URL(ctx context.Context, slug string, d *Deployment) string {
	return s.reach(ctx, slug, d).URL
}

// reach is where people reach a deployment now, and whether only on the
// AppTown host (reachedAt).
func (s *Service) reach(ctx context.Context, slug string, d *Deployment) reach {
	t, err := s.Targets.ByID(ctx, d.TargetID)
	if err != nil {
		t = nil
	}
	return s.reachedAt(ctx, slug, d, t, "")
}

// reachedAt is where people reach a deployment. A standing Docker
// deployment with a published port is reached through its host as it is configured now: this
// AppTown's own host by this AppTown's routes (a tier domain, or its public host
// and the port); an attached host by that host (its ingress domain, or its
// hostname and the port), never this AppTown's address, which serves no app
// on another host (vhost.go). Any other deployment (a preview, one on a
// provider's platform) is reached where it was said to be: observed, else
// recorded.
//
// On this AppTown's own host, an address at the published port (no tier
// domain for it) answers there only, which the Reach says.
func (s *Service) reachedAt(ctx context.Context, _ string, d *Deployment, t *Target, observed string) reach {
	if t != nil && t.Kind == KindDockerHost && d.Standing() && d.HostPort != 0 {
		routes := s.env.RoutesFor(ctx, d.ProjectID)
		servingLabel := ""
		attachedDomain, _ := t.Ingress["domain"].(string)
		if d.Name == "main" && (t.Origin == OriginBuiltIn && routes.MainDomain != "" || t.Origin == OriginAttached && attachedDomain != "") {
			owner, err := project.ByIDIn(ctx, s.db, d.ProjectID)
			if err != nil {
				log.Printf("deployment %s: reading its serving identity: %v", d.ID, err)
				return reach{}
			}
			servingLabel = owner.ServingLabel
		}
		if reach, err := dockerReach(routes, t, servingLabel, d.Name, d.HostPort); err == nil {
			return reach
		}
	}
	if observed != "" {
		return reach{URL: observed}
	}
	return reach{URL: d.URL}
}

// dockerReach is the one place a Docker deployment's address is chosen, for
// the driver, the landing and reads alike: on this AppTown's own host by its
// routes (main at its domain when it has one, else the published port,
// which answers there only); on an attached host by that host's front door.
func dockerReach(routes Routing, t *Target, servingLabel, name string, port int) (reach, error) {
	switch t.Origin {
	case OriginBuiltIn:
		if name == "main" && routes.MainDomain != "" {
			return reach{URL: routes.DeploymentURL(servingLabel, port)}, nil
		}
		return reach{URL: routes.publicURL(port), HostOnly: routes.PortAddressesHostOnly()}, nil
	case OriginAttached:
		addr, _, err := hostTargetAddress(t)
		if err != nil {
			return reach{}, err
		}
		return reach{URL: attachedFrontDoor(t, addr.Host).FrontDoor(servingLabel, name, port)}, nil
	}
	return reach{}, fmt.Errorf("Docker target %s has no supported connection", t.Name)
}

// attachedFrontDoor is an attached host as its front door is named: its
// hostname and ingress domain.
func attachedFrontDoor(t *Target, hostname string) docker.Scope {
	domain, _ := t.Ingress["domain"].(string)
	return docker.Scope{Hostname: hostname, Domain: domain}
}
