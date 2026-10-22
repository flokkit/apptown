package secret

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/fault"
)

const instanceValue = "dop_v1_private-instance-value-keep-out"

// instanceService is a Service over a test database whose secrets file
// holds lines, and whose Infisical (configured when the lines name it)
// answers held, or fails while down is set.
func instanceService(t *testing.T, lines string, held map[string]string, down *atomic.Bool) *Service {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	client := infisicalHTTP(func(r *http.Request) (*http.Response, error) {
		if down != nil && down.Load() {
			return secretResponse(503, "unavailable")
		}
		if r.Method == "POST" {
			return secretResponse(200, `{"accessToken":"token","expiresIn":3600}`)
		}
		var parts []string
		for k, v := range held {
			parts = append(parts, `{"secretKey":"`+k+`","secretValue":"`+v+`"}`)
		}
		return secretResponse(200, `{"secrets":[`+strings.Join(parts, ",")+`]}`)
	})
	return NewService(testDB(t), Config{DataDir: dir, Client: client})
}

const infisicalLines = "INFISICAL_URL=https://secrets.test\nINFISICAL_CLIENT_ID=identity\nINFISICAL_CLIENT_SECRET=bootstrap\nINFISICAL_PROJECT_ID=project\n"

func instanceReceipts(t *testing.T, s *Service) []audit.Entry {
	t.Helper()
	got, err := audit.Recent(context.Background(), s.Vault.db, audit.Filter{KindPrefix: "secret.instance-"})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// An instance secret set through the door is what runtime lookup reads; it
// replaces its own line, keeps every other line, and leaves a receipt that
// names who set which name and never the value. Removing it takes its line
// out, with its own receipt.
//
// Kills: a receipt carrying the value; a set that drops another line; a
// remove that leaves the line; a change with no receipt.
func TestAnInstanceSecretIsSetAndRemovedWithReceiptsAndNoValue(t *testing.T) {
	t.Parallel()
	s := instanceService(t, "# AppTown's credentials\nOTHER=kept\nDIGITALOCEAN_ACCESS_TOKEN=old\n", nil, nil)
	ctx := context.Background()
	if err := s.SetInstance(ctx, "DIGITALOCEAN_ACCESS_TOKEN", instanceValue, "boss"); err != nil {
		t.Fatal(err)
	}
	if got := s.All(ctx); got["DIGITALOCEAN_ACCESS_TOKEN"] != instanceValue || got["OTHER"] != "kept" {
		t.Fatalf("runtime lookup after set: %d names, OTHER=%q", len(got), got["OTHER"])
	}
	raw, err := os.ReadFile(filepath.Join(s.dataDir, "secrets.env"))
	if err != nil || !strings.HasPrefix(string(raw), "# AppTown's credentials\n") || strings.Count(string(raw), "DIGITALOCEAN_ACCESS_TOKEN=") != 1 {
		t.Fatalf("the file after set keeps its comment and one line per name: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(s.dataDir, "secrets.env")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the secrets file must stay 0600: %v %v", fi, err)
	}
	if names, err := s.InstanceNames(ctx); err != nil || names["DIGITALOCEAN_ACCESS_TOKEN"] != InstanceStore {
		t.Fatalf("names: %v %v", names, err)
	}
	if err := s.RemoveInstance(ctx, "DIGITALOCEAN_ACCESS_TOKEN", "boss"); err != nil {
		t.Fatal(err)
	}
	if got := s.All(ctx); got["DIGITALOCEAN_ACCESS_TOKEN"] != "" || got["OTHER"] != "kept" {
		t.Fatalf("runtime lookup after remove still has the token, or lost OTHER")
	}
	if err := s.RemoveInstance(ctx, "DIGITALOCEAN_ACCESS_TOKEN", "boss"); !errors.Is(err, ErrInstanceAbsent) || fault.KindOf(err) != fault.NotFound {
		t.Fatalf("removing an absent secret: %v", err)
	}
	receipts := instanceReceipts(t, s)
	if len(receipts) != 2 || receipts[0].Kind != "secret.instance-removed" || receipts[1].Kind != "secret.instance-set" {
		t.Fatalf("receipts: %+v", receipts)
	}
	for _, r := range receipts {
		if r.Actor != "boss" || r.Subject != "DIGITALOCEAN_ACCESS_TOKEN" || strings.Contains(string(r.Payload), instanceValue) || strings.Contains(string(r.Payload), "old") {
			t.Fatalf("receipt: %+v", r)
		}
	}
	if !strings.Contains(string(receipts[1].Payload), `"replaced":true`) {
		t.Fatalf("the set receipt says it replaced a line: %s", receipts[1].Payload)
	}
}

// A name or value the file cannot hold is refused before anything changes:
// several lines, an empty value, a lower-case name, Infisical's own
// identity.
//
// Kills: a multi-line value written (it would inject a second line); the
// store's own identity overwritten through the door.
func TestAnInstanceSecretTheFileCannotHoldIsRefused(t *testing.T) {
	t.Parallel()
	s := instanceService(t, "OTHER=kept\n", nil, nil)
	for _, in := range [][2]string{{"TOKEN", "one\nINJECTED=two"}, {"TOKEN", "  "}, {"token", "x"}, {"INFISICAL_CLIENT_SECRET", "x"}} {
		if err := s.SetInstance(context.Background(), in[0], in[1], "boss"); fault.KindOf(err) != fault.Invalid {
			t.Fatalf("set %q: %v", in[0], err)
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(s.dataDir, "secrets.env")); string(raw) != "OTHER=kept\n" {
		t.Fatalf("a refused set changed the file: %q", raw)
	}
	if got := instanceReceipts(t, s); len(got) != 0 {
		t.Fatalf("a refused set left receipts: %+v", got)
	}
}

// A name Infisical holds is refused, not shadowed: a line in the file would
// silently win over Infisical's value. When Infisical cannot be read the
// AppTown cannot tell, and says so rather than writing.
//
// Kills: a set that shadows Infisical; an unreadable Infisical taken as
// "not held"; a remove of an Infisical name answered as plain absence.
func TestAnInstanceSecretInfisicalHoldsIsRefused(t *testing.T) {
	t.Parallel()
	var down atomic.Bool
	s := instanceService(t, infisicalLines, map[string]string{"DIGITALOCEAN_ACCESS_TOKEN": "from-infisical"}, &down)
	ctx := context.Background()
	err := s.SetInstance(ctx, "DIGITALOCEAN_ACCESS_TOKEN", instanceValue, "boss")
	if !errors.Is(err, ErrHeldByInfisical) || fault.KindOf(err) != fault.Conflict || !strings.Contains(err.Error(), "Infisical") {
		t.Fatalf("set over an Infisical name: %v", err)
	}
	if got := s.All(ctx)["DIGITALOCEAN_ACCESS_TOKEN"]; got != "from-infisical" {
		t.Fatalf("Infisical's value was shadowed")
	}
	if err := s.RemoveInstance(ctx, "DIGITALOCEAN_ACCESS_TOKEN", "boss"); !errors.Is(err, ErrHeldByInfisical) {
		t.Fatalf("remove of an Infisical name: %v", err)
	}
	if names, err := s.InstanceNames(ctx); err != nil || names["DIGITALOCEAN_ACCESS_TOKEN"] != StoreInfisical || names["INFISICAL_CLIENT_SECRET"] != "" {
		t.Fatalf("names: %v %v", names, err)
	}
	// A name Infisical does not hold goes in the file.
	if err := s.SetInstance(ctx, "PORTAINER_API_KEY", instanceValue, "boss"); err != nil {
		t.Fatal(err)
	}
	// A fresh service has nothing cached: with Infisical down it cannot tell.
	fresh := NewService(s.Vault.db, Config{DataDir: s.dataDir, Client: s.client})
	down.Store(true)
	if err := fresh.SetInstance(ctx, "ANOTHER_TOKEN", instanceValue, "boss"); !errors.Is(err, ErrInfisicalUnknown) || fault.KindOf(err) != fault.Unavailable {
		t.Fatalf("set with Infisical unreadable: %v", err)
	}
	if _, held := fresh.fileSecrets()["ANOTHER_TOKEN"]; held {
		t.Fatal("a set AppTown could not check was written")
	}
	if got := instanceReceipts(t, s); len(got) != 1 || got[0].Subject != "PORTAINER_API_KEY" {
		t.Fatalf("receipts: %+v", got)
	}
}

// A running AppTown whose Infisical refresh fails cannot tell what Infisical
// holds now: its last good map serves running credentials but does not
// authorize a local line that might shadow a name added since.
//
// Kills: the error of a failed refresh ignored when a cached map comes back.
func TestAFailedInfisicalRefreshDoesNotAuthorizeAnInstanceSecret(t *testing.T) {
	t.Parallel()
	var down atomic.Bool
	held := map[string]string{"OTHER": "remote"}
	s := instanceService(t, infisicalLines, held, &down)
	ctx := context.Background()
	if _, err := s.InstanceNames(ctx); err != nil {
		t.Fatal(err)
	}
	held["TOKEN"] = "remote-token"
	s.infisical.mu.Lock()
	s.infisical.fetched = time.Now().Add(-2 * time.Minute)
	s.infisical.mu.Unlock()
	down.Store(true)
	if err := s.SetInstance(ctx, "TOKEN", instanceValue, "boss"); !errors.Is(err, ErrInfisicalUnknown) || fault.KindOf(err) != fault.Unavailable {
		t.Fatalf("set after a failed refresh: %v", err)
	}
	if _, written := s.fileSecrets()["TOKEN"]; written {
		t.Fatal("a set AppTown could not check was written")
	}
	if names, err := s.InstanceNames(ctx); !errors.Is(err, ErrSecretsUnread) || names != nil {
		t.Fatalf("names after a failed refresh: %v %v", names, err)
	}
}

// The instance-secret inventory never reports unknown as empty: a
// configured Infisical that cannot be read, or a secrets file that cannot be
// read, is an error; removal from an unreadable file is not "absent", and
// the unreadable file is left as it is.
//
// Kills: the provider failure discarded into an empty list; the lenient
// file reader used by the door (empty map on any error).
func TestUnreadableSecretsAreUnknownNotEmpty(t *testing.T) {
	t.Parallel()
	var down atomic.Bool
	down.Store(true)
	s := instanceService(t, infisicalLines, map[string]string{"TOKEN": "remote"}, &down)
	if names, err := s.InstanceNames(context.Background()); !errors.Is(err, ErrSecretsUnread) || fault.KindOf(err) != fault.Unavailable || names != nil {
		t.Fatalf("names with Infisical unavailable: %v %v", names, err)
	}

	u := instanceService(t, "", nil, nil)
	path := filepath.Join(u.dataDir, "secrets.env")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// A directory where the file belongs: the reader's non-missing failure
	// branch, as a permission or I/O error would take it.
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if names, err := u.InstanceNames(ctx); !errors.Is(err, ErrSecretsUnread) || names != nil {
		t.Fatalf("names with an unreadable file: %v %v", names, err)
	}
	if err := u.RemoveInstance(ctx, "TOKEN", "boss"); !errors.Is(err, ErrSecretsUnread) {
		t.Fatalf("remove from an unreadable file: %v", err)
	}
	if err := u.SetInstance(ctx, "TOKEN", instanceValue, "boss"); !errors.Is(err, ErrSecretsUnread) {
		t.Fatalf("set into an unreadable file: %v", err)
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		t.Fatalf("the unreadable file was replaced: %v", err)
	}
	if got := instanceReceipts(t, u); len(got) != 0 {
		t.Fatalf("receipts: %+v", got)
	}
	// No file at all is genuinely empty.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if names, err := u.InstanceNames(ctx); err != nil || len(names) != 0 {
		t.Fatalf("names with no file: %v %v", names, err)
	}
}
