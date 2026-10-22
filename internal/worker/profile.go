package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"log"
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/execution"
)

// The worker's profile (design/operations/workers.md, "Private Docker on the
// worker"): what the worker measures of the Docker it
// runs private Docker on when it starts, reported at GET /v1/profile, and
// recorded by AppTown with every attempt admitted with private Docker. A
// request whose recorded profile differs from what the worker measures when
// it would make the private Docker is refused: the worker's Docker was
// upgraded, or changed, under it.

// MeasureProfile measures the worker's profile, with what the operator
// declares (exclusive). The private daemon's image is pulled when it is not
// on the worker, so its id, repository digest and daemon version are known.
// Whether a privileged container runs is probed by running one, on an
// agent worker only, after the privileged mark is written; a check worker
// runs none. What cannot be measured is "unknown", with the reason.
func (s *Server) MeasureProfile(ctx context.Context, exclusive bool) execution.WorkerProfile {
	if s.DindImage != "" {
		if _, err := s.command(ctx, "image", "inspect", "--format", "{{.Id}}", s.DindImage); err != nil {
			if _, err := s.command(ctx, "pull", "-q", s.DindImage); err != nil {
				log.Printf("apptown-worker: pulling %s for its profile: %v", s.DindImage, err)
			}
		}
	}
	p, err := s.measureDocker(ctx)
	if err != nil {
		unknown := "unknown: " + err.Error()
		p = execution.WorkerProfile{OS: unknown, Architecture: unknown, Kernel: unknown, Cgroup: unknown, Docker: unknown, StorageDriver: unknown,
			DindImage: unknown, DindImageID: unknown, DindDigest: unknown, DindVersion: unknown}
	} else {
		p.DindVersion = s.dindVersion(ctx)
	}
	p.Name, p.Exclusive, p.Egress, p.Measured = execution.ProfileWorkerVM, exclusive, "not contained", s.now().UTC()
	p.Privileged = s.probePrivileged(ctx, err == nil)
	return p
}

// probePrivileged runs one privileged container that does nothing, and
// says whether it ran.
func (s *Server) probePrivileged(ctx context.Context, measured bool) string {
	switch {
	case s.Role.Role == RoleCheck:
		return "unknown: a check worker runs no privileged container"
	case s.DindImage == "":
		return "unknown: this worker runs no private daemon"
	case !measured:
		return "unknown: Docker could not be measured"
	}
	if err := PrivilegedMark(s.State, s.Role, s.Now)(); err != nil {
		return "unknown: the privileged mark could not be written: " + err.Error()
	}
	if _, err := s.command(ctx, "run", "--rm", "--privileged", "--network", "none", "--label", dockerlabel.Class+"=profile-probe", "--entrypoint", "true", s.DindImage); err != nil {
		return "no: " + err.Error()
	}
	return "yes"
}

// dindVersion is the version the private daemon's image's own dockerd
// reports, run without a network and without privilege; never read from
// the image's tag.
func (s *Server) dindVersion(ctx context.Context) string {
	if s.DindImage == "" {
		return "none"
	}
	out, err := s.command(ctx, "run", "--rm", "--network", "none", "--label", dockerlabel.Class+"=profile-probe", "--entrypoint", "dockerd", s.DindImage, "--version")
	if err != nil {
		return "unknown: " + err.Error()
	}
	version := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "Docker version "))
	if version == "" {
		return "unknown: dockerd --version said nothing"
	}
	return version
}

// measureDocker measures what private Docker rests on, from Docker itself:
// docker info, and the private daemon's image on the worker (its local id
// and repository digest). The daemon's version is measured apart
// (dindVersion), when the worker starts.
func (s *Server) measureDocker(ctx context.Context) (execution.WorkerProfile, error) {
	out, err := s.command(ctx, "info", "--format", "{{json .}}")
	if err != nil {
		return execution.WorkerProfile{}, err
	}
	var info struct {
		OperatingSystem, OSType, Architecture, KernelVersion string
		CgroupVersion, CgroupDriver, ServerVersion, Driver   string
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return execution.WorkerProfile{}, fmt.Errorf("reading docker info: %w", err)
	}
	known := func(v string) string {
		if strings.TrimSpace(v) == "" {
			return "unknown"
		}
		return v
	}
	p := execution.WorkerProfile{OS: known(strings.TrimSpace(info.OSType + " " + info.OperatingSystem)), Architecture: known(info.Architecture),
		Kernel: known(info.KernelVersion), Docker: known(info.ServerVersion), StorageDriver: known(info.Driver),
		DindImage: "none", DindImageID: "none", DindDigest: "none", DindVersion: "none"}
	p.Cgroup = "unknown"
	if info.CgroupVersion != "" {
		p.Cgroup = "v" + info.CgroupVersion + " (" + known(info.CgroupDriver) + ")"
	}
	if s.DindImage != "" {
		p.DindImage = s.DindImage
		p.DindImageID, p.DindDigest = s.dindImageFacts(ctx)
	}
	return p, nil
}

// dindImageFacts are the private daemon's image's local id and repository
// digest on the worker, each "unknown: ..." when it cannot be read.
func (s *Server) dindImageFacts(ctx context.Context) (id, digest string) {
	out, err := s.command(ctx, "image", "inspect", "--format", "{{.Id}}|{{json .RepoDigests}}", s.DindImage)
	if err != nil {
		unknown := "unknown: not on this worker: " + err.Error()
		return unknown, unknown
	}
	id, digests, _ := strings.Cut(strings.TrimSpace(string(out)), "|")
	id = strings.TrimSpace(id)
	if id == "" {
		id = "unknown: docker image inspect named no id"
	}
	var repo []string
	if err := json.Unmarshal([]byte(digests), &repo); err != nil {
		return id, "unknown: reading its repository digests: " + err.Error()
	}
	if len(repo) == 0 {
		return id, "unknown: the image has no repository digest (it was not pulled from a registry)"
	}
	return id, strings.Join(repo, ",")
}

// errProfileChanged: the worker's profile is not the one the request was
// admitted on.
var errProfileChanged = errors.New("this worker's profile is not the one the task was admitted on")

// sameProfile refuses private Docker admitted on another profile than the
// worker measures now (errProfileChanged), or on none. The daemon's image
// matters only to a request with a daemon. The daemon's version is a fact
// of the image's bytes: while the image's id is the one measured at the
// worker's start, it is the version measured then; otherwise it is
// measured again.
func (s *Server) sameProfile(ctx context.Context, admitted *execution.WorkerProfile, daemon bool) error {
	if admitted == nil {
		return fmt.Errorf("%w: the request names no profile", errProfileChanged)
	}
	now, err := s.measureDocker(ctx)
	if err != nil {
		return fmt.Errorf("measuring this worker's Docker: %w", err)
	}
	now.DindVersion = s.Profile.DindVersion
	if daemon && (now.DindImageID != s.Profile.DindImageID || s.Profile.DindVersion == "") {
		now.DindVersion = s.dindVersion(ctx)
	}
	if !daemon {
		now.DindImage, now.DindImageID, now.DindDigest, now.DindVersion = admitted.DindImage, admitted.DindImageID, admitted.DindDigest, admitted.DindVersion
	}
	if changed := admitted.Changed(now); changed != "" {
		return fmt.Errorf("%w: %s; the step must be admitted again", errProfileChanged, changed)
	}
	return nil
}

// profile is GET /v1/profile: what the worker measured when it started.
func (s *Server) profile(w http.ResponseWriter, _ *http.Request) {
	if s.Profile.Name == "" {
		http.Error(w, "this worker measured no profile when it started", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, s.Profile)
}
