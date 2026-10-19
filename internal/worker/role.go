package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/durable"
)

// A worker's role is fixed when it is provisioned
// (design/operations/workers.md, "A check worker"). An agent worker runs chats, tasks and
// previews, and may run privileged containers; a check worker runs checks
// and nothing else. Gates that need checks independent of agents accept only
// a check worker's results, so a machine becomes one only by being
// provisioned for it: its role is written into an empty state directory
// before anything else, and never changed.
const (
	RoleAgent = "agent"
	RoleCheck = "check"
)

// Role is what state/role records, and GET /v1/role reports.
type Role struct {
	Role  string    `json:"role"`
	Since time.Time `json:"since"`
}

// FixRole returns the role of the worker whose state directory is state,
// started as want. An empty or missing directory is fixed as want now, on
// the worker's clock (nil is the real one). A
// directory with a role must have want. One with state but no role is a
// worker from before roles, which may have run privileged chats: an agent
// worker, which can never become a check worker.
func FixRole(state, want string, now func() time.Time) (Role, error) {
	if want != RoleAgent && want != RoleCheck {
		return Role{}, fmt.Errorf("-role %q: a worker is an agent or a check worker", want)
	}
	entries, err := os.ReadDir(state)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Role{}, err
	}
	var role Role
	body, err := os.ReadFile(filepath.Join(state, "role"))
	switch {
	case err == nil:
		if err := json.Unmarshal(body, &role); err != nil {
			return Role{}, fmt.Errorf("%s: %w", filepath.Join(state, "role"), err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return Role{}, err
	case len(entries) > 0 && want == RoleCheck:
		return Role{}, fmt.Errorf("%s holds a worker's state but no role: it was an agent worker, which may have run privileged chats, and can never become a check worker; provision a check worker with a new state directory", state)
	default:
		role = Role{Role: want, Since: clock.Or(now)().UTC()}
		if body, err = json.Marshal(role); err == nil {
			err = writeDurableNew(state, "role", body)
		}
		if err != nil {
			return Role{}, fmt.Errorf("fixing the worker's role: %w", err)
		}
	}
	if role.Role != want {
		return Role{}, fmt.Errorf("this worker was provisioned as an %s worker at %s; it cannot start as a %s worker: a role is never changed, only provisioned again with a new state directory", role.Role, role.Since.Format(time.RFC3339), want)
	}
	return role, nil
}

// PrivilegedMark is the guard an agent worker runs before it creates any
// privileged container: state/privileged-agents is written, durably, first,
// so a crash after the write leaves it, and a failed write creates nothing.
// It is a guard on an agent-controlled machine, not an attestation. A check
// worker creates no privileged container. The mark's time is the worker's
// clock's (nil is the real one).
func PrivilegedMark(state string, role Role, now func() time.Time) func() error {
	return func() error {
		if role.Role == RoleCheck {
			return errors.New("a check worker creates no privileged container")
		}
		body, _ := json.Marshal(struct {
			First time.Time `json:"first"`
		}{clock.Or(now)().UTC()})
		return writeDurableNew(state, "privileged-agents", body)
	}
}

// writeDurableNew writes dir/name once: an existing file is kept, and its
// directory synced before it is relied on.
func writeDurableNew(dir, name string, body []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	existed, err := durable.Write(dir, name, body, 0o600, true)
	if err == nil && existed {
		err = durable.SyncDir(dir)
	}
	return err
}

// role is GET /v1/role.
func (s *Server) role(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.Role)
}

// FirstUse is when this forge first used the worker at url, recorded under
// dir the first time it is asked. The record is kept whatever role the
// worker had then, so a machine the forge once used as an agent worker, and
// then gave a new state directory, is not taken for a fresh check worker.
// The first use is stamped on the forge's clock (nil is the real one).
func FirstUse(dir, url string, now func() time.Time) (time.Time, error) {
	type use struct {
		URL   string    `json:"url"`
		First time.Time `json:"first"`
	}
	sum := sha256.Sum256([]byte(url))
	name := hex.EncodeToString(sum[:8]) + ".json"
	body, _ := json.Marshal(use{url, clock.Or(now)().UTC()})
	err := writeDurableNew(dir, name, body)
	var kept use
	if err == nil {
		body, err = os.ReadFile(filepath.Join(dir, name))
	}
	if err == nil {
		err = json.Unmarshal(body, &kept)
	}
	if err == nil && kept.URL != url {
		err = fmt.Errorf("the record names %s", kept.URL)
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("this AppTown's first use of the worker at %s: %w", url, err)
	}
	return kept.First, nil
}

// RequireCheckWorker refuses a check worker that does not report the check
// role, fixed before this forge first used its address (first).
func RequireCheckWorker(ctx context.Context, client *http.Client, url string, first time.Time) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/v1/role", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("reading the check worker's role: %w", err)
	}
	defer resp.Body.Close()
	var role Role
	if resp.StatusCode != http.StatusOK {
		// The worker's own words name what is wrong (a 426 names the
		// protocols on both sides and what to install).
		said, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("reading the check worker's role: %s: %s", resp.Status, strings.TrimSpace(string(said)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&role); err != nil {
		return fmt.Errorf("reading the check worker's role: %w", err)
	}
	if role.Role != RoleCheck {
		return fmt.Errorf("the check worker at %s is not a check worker: it reports the role %q", url, role.Role)
	}
	if !role.Since.Before(first) {
		return fmt.Errorf("the check worker at %s was made a check worker at %s, after this AppTown first used that address at %s: a machine AppTown used before is not a fresh check worker",
			url, role.Since.Format(time.RFC3339), first.Format(time.RFC3339))
	}
	return nil
}
