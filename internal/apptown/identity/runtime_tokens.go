package identity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/durable"
)

// Workload identifies the admitted work a runtime credential represents.
// Actor is receipt attribution. Person, when set, is the account whose
// authority the workload exercises: a draft asked through a console is
// attributed to "alice via console/<id>" but acts as alice, and SessionID
// then names that console, whose lifecycle it also needs.
type Workload struct {
	Actor  string
	Person string
	// Principal is the person the workload acts for (a run's owner, a
	// chat's opener), judged by ID; zero when it acts for AppTown or for
	// no principal AppTown could tie it to.
	Principal ids.ID
	Kind      string
	ProjectID ids.ID
	RunID     ids.ID
	StepID    ids.ID
	Attempt   int64
	SessionID string
}

// Who is the actor the workload's acts are recorded against: its principal
// as it was admitted and the way it acts (a run step, a chat), never its
// name looked up again.
func (w Workload) Who() access.Actor {
	a := access.Actor{Principal: w.Principal, Name: w.Actor}
	switch w.Kind {
	case "step":
		a.Via = access.ViaRunStep
	case "console":
		a.Via = access.ViaChat + ":" + w.SessionID
	case "assist":
		if w.SessionID != "" {
			a.Via = access.ViaChat + ":" + w.SessionID
		}
	}
	return a
}

// RuntimeTokenKeyFile is the name, under AppTown's data directory, of the
// key step tokens are derived under.
const RuntimeTokenKeyFile = "runtime-token-key"

const (
	runtimeTokenKeySize = 32
	stepTokenPrefix     = "rt_"
	stepTokenMACHex     = 32 // 128 bits of HMAC-SHA256, hex
)

// Runtime tokens: build agents were the only actors in the
// system with NO credential — consoles have session tokens, humans have
// logins, so the CLI surface the agents are steered to use (apptown check,
// config validate, status) either 401'd or had to be opened wide.
//
// A step attempt's token is derived, not stored:
// rt_<step>_<attempt>_<mac>, the MAC an HMAC of the step and attempt under a
// key kept in the data directory. The same attempt always gets the same
// token, before and after an AppTown restart, so an agent still running on a
// worker keeps a credential AppTown recognises, and a recovered request
// keeps its digest. The token proves only which attempt it was issued for;
// whether that attempt may still act is read from current state by the one
// validation both doors use (inference.Identity.AuthenticateStep). There is
// no revocation list: a settled, cancelled or superseded attempt is refused
// by its state.
//
// Draft-assist grants are not step attempts: they stay process-local, minted
// and revoked around one assist call, and never reach the API door.
type RuntimeTokens struct {
	key  []byte
	mu   sync.Mutex
	byTk map[string]Workload // assist token -> workload identity
}

// Open loads the step-token key from file, creating it (32 random bytes,
// 0600) when absent. Any other failure to read or create it is returned:
// without the key no step credential can be issued or checked, so AppTown
// refuses to start rather than run agents it cannot recognise. Until a key
// is loaded no step token is recognised.
func (tokens *RuntimeTokens) Open(file string) error {
	key, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		key, err = createRuntimeTokenKey(file)
	}
	if err != nil {
		return fmt.Errorf("runtime token key %s: %w", file, err)
	}
	if len(key) != runtimeTokenKeySize {
		return fmt.Errorf("runtime token key %s: %d bytes, want %d", file, len(key), runtimeTokenKeySize)
	}
	tokens.mu.Lock()
	tokens.key = key
	tokens.mu.Unlock()
	return nil
}

// createRuntimeTokenKey writes a new key durably and publishes it with a
// hard link, so a concurrent or interrupted creation never leaves a partial
// key under the final name; an existing key is kept and returned.
func createRuntimeTokenKey(file string) ([]byte, error) {
	key := make([]byte, runtimeTokenKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	existed, err := durable.Write(filepath.Dir(file), filepath.Base(file), key, 0o600, true)
	if existed {
		return os.ReadFile(file)
	}
	if err != nil {
		return nil, err
	}
	return key, nil
}

// Step is the token of one step attempt. It is the same on every call and
// after every restart that reads the same key.
func (tokens *RuntimeTokens) Step(stepID ids.ID, attempt int64) string {
	return fmt.Sprintf("%s%d_%d_%s", stepTokenPrefix, stepID, attempt, tokens.stepMAC(stepID, attempt))
}

func (tokens *RuntimeTokens) stepMAC(stepID ids.ID, attempt int64) string {
	mac := hmac.New(sha256.New, tokens.stepKey())
	fmt.Fprintf(mac, "step/%d/%d", stepID, attempt)
	return hex.EncodeToString(mac.Sum(nil))[:stepTokenMACHex]
}

// StepAttempt reports the step attempt a token was issued for, when its MAC
// holds. It says nothing about whether the attempt may still act: callers
// admit only through the current-state validation.
func (tokens *RuntimeTokens) StepAttempt(token string) (stepID ids.ID, attempt int64, ok bool) {
	if len(tokens.stepKey()) == 0 || !strings.HasPrefix(token, stepTokenPrefix) {
		return ids.Nil, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(token, stepTokenPrefix), "_")
	if len(parts) != 3 {
		return ids.Nil, 0, false
	}
	stepID, err := ids.Parse(parts[0])
	if err != nil || stepID.IsZero() || stepID.String() != parts[0] {
		return ids.Nil, 0, false
	}
	attempt, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil || attempt < 0 || strconv.FormatInt(attempt, 10) != parts[1] {
		return ids.Nil, 0, false
	}
	if !hmac.Equal([]byte(parts[2]), []byte(tokens.stepMAC(stepID, attempt))) {
		return ids.Nil, 0, false
	}
	return stepID, attempt, true
}

func (tokens *RuntimeTokens) stepKey() []byte {
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	return tokens.key
}

// Mint issues a process-local token for a non-step workload (a draft
// assist); Revoke ends it.
func (tokens *RuntimeTokens) Mint(who Workload) string {
	buf := make([]byte, 24)
	_, _ = rand.Read(buf)
	tok := "run_" + hex.EncodeToString(buf)
	tokens.mu.Lock()
	if tokens.byTk == nil {
		tokens.byTk = map[string]Workload{}
	}
	tokens.byTk[tok] = who
	tokens.mu.Unlock()
	return tok
}

func (tokens *RuntimeTokens) Revoke(tok string) {
	tokens.mu.Lock()
	delete(tokens.byTk, tok)
	tokens.mu.Unlock()
}

func (tokens *RuntimeTokens) Lookup(token string) (Workload, bool) {
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	who, ok := tokens.byTk[token]
	return who, ok
}
