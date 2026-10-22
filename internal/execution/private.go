package execution

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
)

// PrivateDocker is what a task has of its own beside its container, from
// its start to its exit (design/operations/workers.md, "Private Docker on
// the worker"):
// a network, the services on it, and, when Daemon is set, a private Docker
// daemon the task reaches as dind. The services are resolved when the task
// is admitted and travel in its request, so a request sent again runs the
// same ones whatever the project or the catalog says later.
type PrivateDocker struct {
	Daemon   bool                 `json:"daemon,omitempty"`
	Services []deploy.ServiceSpec `json:"services,omitempty"`
	// Mirror is the Docker Hub mirror the daemon and its builder pull
	// through ("" none). Resolvers are the backend's own.
	Mirror string `json:"mirror,omitempty"`
	// Profile is the worker's profile the task was admitted on, as the
	// worker reported it. A worker refuses the request when what it
	// measures now differs (WorkerProfile.Changed).
	Profile *WorkerProfile `json:"profile,omitempty"`
}

// ProfileWorkerVM is the first worker profile for private Docker: a machine the installation runs its
// worker on, whose Docker runs privileged daemons, one per invocation.
const ProfileWorkerVM = "worker-vm"

// WorkerProfile is what a worker measured of the Docker it runs private
// Docker on, when it started, and what its operator declared. A fact that
// could not be measured is "unknown", never a guess.
type WorkerProfile struct {
	Name          string `json:"name"`
	OS            string `json:"os"`
	Architecture  string `json:"architecture"`
	Kernel        string `json:"kernel"`
	Cgroup        string `json:"cgroup"`
	Docker        string `json:"docker"`
	StorageDriver string `json:"storageDriver"`
	// DindImage is the private daemon's image as the worker is configured
	// with it (a tag, such as docker:27-dind), or "none".
	DindImage string `json:"dindImage"`
	// DindImageID is Docker's local id of that image on the worker: what
	// the worker compares to notice the image changed under a request.
	DindImageID string `json:"dindImageId"`
	// DindDigest is the image's repository digest (RepoDigests), as pulled
	// from its registry; "unknown: ..." for an image without one.
	DindDigest string `json:"dindDigest"`
	// DindVersion is the version the image's own dockerd reports.
	DindVersion string `json:"dindVersion"`
	// Privileged says whether a privileged container ran when probed.
	Privileged string `json:"privileged"`
	// Exclusive is declared by the operator (apptown-worker -exclusive): the
	// machine is the installation's alone. It is not measured.
	Exclusive bool `json:"exclusive"`
	// Egress is never contained in this profile.
	Egress   string    `json:"egress"`
	Measured time.Time `json:"measured"`
}

// Changed says how now differs from p in what private Docker rests on: the
// operating system, architecture and kernel, the Docker version, the
// storage driver, the cgroup mode and the private daemon's image (its id,
// repository digest and daemon version). The time of measurement does not
// matter. "" is the same.
func (p WorkerProfile) Changed(now WorkerProfile) string {
	var changed []string
	for _, f := range []struct{ what, was, is string }{
		{"OS", p.OS, now.OS},
		{"architecture", p.Architecture, now.Architecture},
		{"kernel", p.Kernel, now.Kernel},
		{"Docker", p.Docker, now.Docker},
		{"storage driver", p.StorageDriver, now.StorageDriver},
		{"cgroup mode", p.Cgroup, now.Cgroup},
		{"private daemon image", p.DindImage, now.DindImage},
		{"private daemon image id", p.DindImageID, now.DindImageID},
		{"private daemon image digest", p.DindDigest, now.DindDigest},
		{"private daemon version", p.DindVersion, now.DindVersion},
	} {
		if f.was != f.is {
			changed = append(changed, fmt.Sprintf("%s %q is now %q", f.what, f.was, f.is))
		}
	}
	return strings.Join(changed, "; ")
}

var serviceName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Valid refuses what no backend creates: a service without an image or a
// valid name, two with one name, one named dind (the daemon's name on the
// network), and a limit left at zero, which is never "unlimited" here.
func (p *PrivateDocker) Valid(l Limits) error {
	if l.CPUs <= 0 || l.MemoryBytes <= 0 || l.PIDs <= 0 {
		return fmt.Errorf("private Docker needs CPU, memory and PID limits: zero is not unlimited (%+v)", l)
	}
	seen := map[string]bool{}
	for _, s := range p.Services {
		switch {
		case !serviceName.MatchString(s.Name) || s.Image == "":
			return fmt.Errorf("the service %q needs a name of lower-case letters, digits and dashes, and an image", s.Name)
		case s.Name == "dind":
			return fmt.Errorf("a service cannot be named dind: that is the private daemon's name")
		case seen[s.Name]:
			return fmt.Errorf("two services are named %s", s.Name)
		}
		seen[s.Name] = true
	}
	return nil
}
