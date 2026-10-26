package instanceupdate

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/sigstore/sigstore-go/pkg/root"
	"golang.org/x/mod/semver"
)

// Options binds one installation's notification owner. Transport injection is
// assembly input, not a destination or credential accepted from an HTTP caller.
type Options struct {
	DataDir   string
	Build     string
	Platform  string
	Channel   string
	Authority access.Authorizer
	Now       func() time.Time
	Transport http.RoundTripper
}

type Service struct {
	mu       sync.Mutex
	checking bool
	options  Options
	cache    cacheStore
	source   releaseSource
}

type Candidate struct {
	Version  string   `json:"version"`
	Commit   string   `json:"commit"`
	Platform string   `json:"platform"`
	Notes    string   `json:"notes"`
	Archive  Artifact `json:"archive"`
	Evidence Evidence `json:"evidence"`
}

type Status struct {
	Build       string     `json:"build"`
	Platform    string     `json:"platform"`
	Channel     string     `json:"channel"`
	State       string     `json:"state"`
	LastAttempt *time.Time `json:"lastAttempt,omitempty"`
	LastSuccess *time.Time `json:"lastSuccess,omitempty"`
	Candidate   *Candidate `json:"candidate,omitempty"`
	ReasonCode  string     `json:"reasonCode,omitempty"`
	Reason      string     `json:"reason,omitempty"`
	// U1 never confuses a verified candidate with enrolled recovery/replacement.
	AutomaticReplacement bool   `json:"automaticReplacement"`
	ReplacementReason    string `json:"replacementReason"`
}

func New(in Options) (*Service, error) {
	if in.Channel == "" {
		in.Channel = "stable"
	}
	if in.Channel != "stable" && in.Channel != "prerelease" {
		return nil, fmt.Errorf("release channel must be stable or prerelease")
	}
	in.Now = clock.Or(in.Now)
	if in.DataDir == "" || in.Build == "" || in.Platform == "" {
		return nil, fmt.Errorf("release notification needs installed build, platform and data directory")
	}
	data, err := filepath.Abs(in.DataDir)
	if err != nil {
		return nil, err
	}
	h := newPublicHTTP(in.Transport)
	s := &Service{options: in, cache: cacheStore{dir: filepath.Join(data, "instance-updates")}}
	s.source = releaseSource{http: h, verify: releaseVerifier{trust: func(ctx context.Context) (root.TrustedMaterial, error) {
		return h.trustedRoot(ctx, filepath.Join(s.cache.dir, "trust"))
	}}}
	return s, nil
}

func (s *Service) admit(ctx context.Context) error {
	if s.options.Authority == nil {
		return access.ErrAuthorityUnavailable
	}
	_, err := s.options.Authority.Require(ctx, access.Permission{Capability: access.CapInstanceUpdates, Resource: access.Resource{Type: "instance", ID: "instance"}})
	return err
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	if err := s.admit(ctx); err != nil {
		return Status{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.cache.read(s.options.Channel)
	if err != nil {
		return Status{}, fault.Wrap(fault.Unavailable, fmt.Errorf("release status is unavailable: %w", err))
	}
	return s.view(st), nil
}

func (s *Service) view(st state) Status {
	v := Status{Build: s.options.Build, Platform: s.options.Platform, Channel: s.options.Channel, State: "unknown", LastAttempt: st.LastAttempt, LastSuccess: st.LastSuccess, Candidate: st.Candidate, Reason: st.Failure, ReasonCode: st.FailureCode, ReplacementReason: "Automatic replacement requires a qualified updater and authenticated installed-build provenance."}
	if st.Candidate != nil && st.Candidate.Platform != s.options.Platform {
		v.Candidate = nil
		v.State = "unknown"
		v.Reason = "The installed platform changed; check releases again."
	} else if st.LastSuccess != nil {
		if st.Candidate == nil {
			v.State = "no-release"
		} else if validTag(s.options.Build) && semver.Compare(st.Candidate.Version, s.options.Build) <= 0 {
			v.State = "current"
		} else {
			v.State = "available"
		}
		now := s.options.Now()
		if st.Failure != "" || now.Sub(*st.LastSuccess) > 26*time.Hour || st.LastSuccess.After(now.Add(5*time.Minute)) || (st.LastAttempt != nil && st.LastAttempt.After(*st.LastSuccess)) {
			v.State = "stale"
		}
	} else if st.Failure != "" {
		v.State = "unavailable"
	}
	if s.checking {
		v.State = "checking"
	}
	return v
}

// Check is an explicit administrator request. Concurrent or repeated requests
// share the owner's cooldown rather than creating unbounded public fetches.
func (s *Service) Check(ctx context.Context) (Status, error) {
	if err := s.admit(ctx); err != nil {
		return Status{}, err
	}
	return s.check(ctx, true)
}

func (s *Service) check(ctx context.Context, manual bool) (Status, error) {
	s.mu.Lock()
	if s.checking {
		s.mu.Unlock()
		return Status{}, fault.New(fault.Unavailable, "a release check is already running")
	}
	st, err := s.cache.read(s.options.Channel)
	if err != nil {
		s.mu.Unlock()
		return Status{}, fault.Wrap(fault.Unavailable, fmt.Errorf("release status is unavailable: %w", err))
	}
	now := s.options.Now().UTC()
	if st.LastAttempt != nil && now.Sub(*st.LastAttempt) < time.Minute {
		v := s.view(st)
		s.mu.Unlock()
		return v, fault.New(fault.Unavailable, "wait a minute before checking releases again")
	}
	if !manual && !due(st, now, s.options.Platform) {
		v := s.view(st)
		s.mu.Unlock()
		return v, nil
	}
	st.LastAttempt = &now
	st.Failure = ""
	st.FailureCode = ""
	if err := s.cache.save(st); err != nil {
		s.mu.Unlock()
		return Status{}, fault.Wrap(fault.Unavailable, fmt.Errorf("cannot record release check: %w", err))
	}
	s.checking = true
	s.mu.Unlock()

	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	tag, pages, checkErr := s.source.discover(checkCtx, s.options.Channel, st.Pages)
	var candidate *Candidate
	if checkErr == nil && tag != "" {
		var release signedRelease
		release, checkErr = s.source.fetch(checkCtx, tag)
		if checkErr == nil {
			archive, ok := release.Manifest.Archives[s.options.Platform]
			if !ok {
				checkErr = fmt.Errorf("release does not support this platform")
			} else if checkErr = s.cache.retain(release); checkErr == nil {
				candidate = &Candidate{Version: release.Manifest.Version, Commit: release.Manifest.Commit, Platform: s.options.Platform, Notes: string(release.Notes), Archive: archive.Artifact, Evidence: release.Evidence}
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checking = false
	if ctx.Err() != nil {
		return s.view(st), ctx.Err()
	}
	if checkErr != nil {
		// Never persist remote certificate text or local filesystem paths in
		// the user-facing status. Failed authentication is not no-update.
		st.FailureCode, st.Failure = failureReason(checkErr)
	} else {
		finished := s.options.Now().UTC()
		st.LastSuccess = &finished
		st.Candidate = candidate
		st.Pages = pages
	}
	if err := s.cache.save(st); err != nil {
		return s.view(st), fault.Wrap(fault.Unavailable, fmt.Errorf("cannot record verified release status: %w", err))
	}
	if checkErr != nil {
		return s.view(st), fault.New(fault.Unavailable, "release check failed; previous verified evidence was retained")
	}
	return s.view(st), nil
}

func due(st state, now time.Time, platform string) bool {
	return st.LastSuccess == nil || st.Failure != "" || st.LastAttempt == nil || st.LastAttempt.After(*st.LastSuccess) || now.Sub(*st.LastSuccess) >= 24*time.Hour || (st.Candidate != nil && st.Candidate.Platform != platform)
}

// Run is public-information discovery under daemon lifetime, never updater
// execution. The normal daemon calls it; verify-only startup does not.
func (s *Service) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		if _, err := s.check(ctx, false); err != nil && ctx.Err() == nil {
			log.Printf("release discovery: %v", err) // dependency failures leave notification unavailable, not the daemon
		}
		var entropy [8]byte
		delay := time.Hour
		if _, err := rand.Read(entropy[:]); err == nil {
			delay += time.Duration(binary.LittleEndian.Uint64(entropy[:]) % uint64(15*time.Minute))
		} // Jitter is optional; notification scheduling must not stop the daemon.
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func failureReason(err error) (string, string) {
	switch {
	case errors.Is(err, errMetadataMissing):
		return "metadata-missing", "The published release does not include complete authenticated update metadata. Use the manual upgrade guide."
	case errors.Is(err, errAuthentication):
		return "authentication-failed", "Release authentication failed. Previous verified evidence was retained."
	default:
		return "check-unavailable", "Could not complete the release check. Previous verified evidence was retained."
	}
}
