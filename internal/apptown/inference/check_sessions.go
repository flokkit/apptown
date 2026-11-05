package inference

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/clock"
)

// CheckSessions admits inference for one bounded check task. Tokens open only
// inference, never the API. Closing a task revokes its credential. Sessions are
// deliberately not recovered after a daemon restart.
type CheckSessions struct {
	Now      func() time.Time
	mu       sync.Mutex
	sessions map[string]*checkSession
}
type checkSession struct {
	task, model string
	purposeName string
	judge       bool
	who         identity.Workload
	expires     time.Time
	cap         int64
	busy        bool
	turnDone    chan struct{}
	report      CheckUsage
}

// CheckUsage is observed by the gateway, never supplied by the agent.
type CheckUsage struct {
	modelcall.Usage
	Reason string `json:"reason,omitempty"`
	// EvidenceFailure distinguishes provider uncertainty and caps from a task
	// that never made an inference request.
	EvidenceFailure bool `json:"evidenceFailure,omitempty"`
}

// Open mints inference-only authority for trusted server code executing one
// task. Call supplies its model and spend attribution, including the recorded
// eval judge where applicable. Close or expiry revokes it.
func (s *CheckSessions) Open(task string, call modelcall.Call, cap int64, expires time.Time) (string, error) {
	if task == "" || call.Model == "" || cap <= 0 || !clock.Or(s.Now)().Before(expires) {
		return "", fmt.Errorf("a check credential needs its task, model, token allowance and expiry")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := "check_" + hex.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		s.sessions = map[string]*checkSession{}
	}
	actor := call.Actor
	if actor == "" {
		actor = "apptown-system"
	}
	purpose := call.Purpose
	if purpose == "" {
		purpose = "check"
	}
	s.sessions[token] = &checkSession{task: task, model: call.Model, purposeName: purpose, judge: call.Judge, cap: cap, expires: expires, who: identity.Workload{Kind: "check", Actor: actor, ProjectID: call.ProjectID, RunID: call.RunID, SessionID: task}}
	return token, nil
}
func (s *CheckSessions) authenticate(token string) (identity.Workload, bool) {
	if s == nil {
		return identity.Workload{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.sessions[token]
	if c == nil || !clock.Or(s.Now)().Before(c.expires) || c.report.Reason != "" {
		return identity.Workload{}, false
	}
	return c.who, true
}

// begin serializes a session's provider turns, including SDK background work,
// so every new turn sees the preceding turn's complete allowance accounting.
func (s *CheckSessions) begin(ctx context.Context, token, pattern string) error {
	for {
		s.mu.Lock()
		c := s.sessions[token]
		if c == nil || !clock.Or(s.Now)().Before(c.expires) {
			s.mu.Unlock()
			return fmt.Errorf("check session is no longer active")
		}
		if c.report.Reason != "" {
			reason := c.report.Reason
			s.mu.Unlock()
			return fmt.Errorf("%s", reason)
		}
		if c.model != pattern {
			s.mu.Unlock()
			return fmt.Errorf("this check session may use only its selected model")
		}
		if !c.busy {
			c.busy = true
			c.turnDone = make(chan struct{})
			s.mu.Unlock()
			return nil
		}
		done, expires := c.turnDone, c.expires
		s.mu.Unlock()
		timer := time.NewTimer(expires.Sub(clock.Or(s.Now)()))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("check session is no longer active")
		case <-done:
			timer.Stop()
		}
	}
}

func (s *CheckSessions) finish(token, served string, u inferenceUsage, incomplete string, admitted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.sessions[token]
	if c == nil {
		return
	}
	if c.busy {
		c.busy = false
		close(c.turnDone)
	}
	// A refused request consumed no known tokens and may be retried.
	if !admitted && !u.Observed {
		return
	}
	unpriced := ""
	if !u.Priced && u.Input+u.Output > 0 {
		unpriced = "the provider reported tokens with no price"
	}
	c.report.Usage.Add(&modelcall.Usage{Prompt: u.Input + u.CacheRead + u.CacheWrite, Completion: u.Output, Cost: u.Cost, PriceSource: u.PriceSource, Incomplete: unpriced})
	if served != "" {
		if c.report.Model != "" && c.report.Model != served {
			c.report.Reason = "check model changed during its session"
			c.report.EvidenceFailure = true
		}
		c.report.Model = served
	}
	if !u.Observed {
		c.report.Incomplete = incomplete
		if c.report.Incomplete == "" {
			c.report.Incomplete = "the provider reported no usage"
		}
		c.report.Reason = c.report.Incomplete
		c.report.EvidenceFailure = true
	}
	if c.report.Prompt+c.report.Completion >= c.cap {
		c.report.Reason = fmt.Sprintf("check did not finish: token limit of %d reached", c.cap)
		c.report.EvidenceFailure = true
	}
}

// Close revokes the credential and returns the gateway's observations. A token
// cannot be closed or inspected under another task's identity.
func (s *CheckSessions) Close(token, task string) (CheckUsage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.sessions[token]
	if c == nil || c.task != task {
		return CheckUsage{}, fmt.Errorf("unknown check task credential")
	}
	delete(s.sessions, token)
	if c.busy {
		c.busy = false
		close(c.turnDone)
		c.report.Reason = "check inference has not finished"
		c.report.EvidenceFailure = true
		c.report.Incomplete = c.report.Reason
	}
	if c.report.Model == "" && c.report.Reason == "" {
		c.report.Reason = "check session made no observed inference requests"
	}
	return c.report, nil
}

// checkSession authenticates a credential only for the inference door.
func (a Identity) checkSession(token string) (identity.Workload, error) {
	if who, ok := a.Checks.authenticate(token); ok {
		return who, nil
	}
	return identity.Workload{}, errDenied
}

func (s *CheckSessions) isJudge(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.sessions[token]
	return c != nil && c.judge
}

func (s *CheckSessions) purpose(token string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.sessions[token]
	if c == nil {
		return ""
	}
	return c.purposeName
}
