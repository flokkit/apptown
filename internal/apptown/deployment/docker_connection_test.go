package deployment

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestParseSSHAddressAndStanza(t *testing.T) {
	t.Parallel()
	a, err := parseSSHAddress("ssh://deploy@droplet.example.com:2222")
	if err != nil || a.User != "deploy" || a.Host != "droplet.example.com" || a.Port != 2222 {
		t.Fatalf("parse: %+v %v", a, err)
	}
	if a, err := parseSSHAddress("ssh://root@10.0.0.5"); err != nil || a.Port != 22 {
		t.Fatalf("default port: %+v %v", a, err)
	}
	for _, bad := range []string{"tcp://x:2376", "root@host", "ssh://host", "ssh://a@b:70000", "ssh://a@b; rm -rf /"} {
		if _, err := parseSSHAddress(bad); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	// A block AppTown wrote before, with another key.
	targetName := sshTargetName("box", "/c/socket")
	earlier := fmt.Sprintf("# BEGIN apptown target %s\nHost %s\n  IdentityFile /k/zero\n# END apptown target %s\n", targetName, sshAlias(targetName), targetName)
	if err := os.WriteFile(cfg, []byte("Host mine\n  User me\n\n"+earlier), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := upsertSSHStanza(cfg, "box", "/c/socket", sshStanza("box", a, "/k/one", "/c/socket")); err != nil {
		t.Fatal(err)
	}
	if err := upsertSSHStanza(cfg, "box", "/c/socket", sshStanza("box", a, "/k/two", "/c/socket")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(cfg)
	s := string(got)
	if strings.Count(s, "BEGIN apptown target box") != 1 || !strings.Contains(s, "IdentityFile \"/k/two\"") || strings.Contains(s, "/k/one") || !strings.Contains(s, "Host mine\n  User me\n") {
		t.Fatalf("AppTown replaces exactly its own block and keeps the rest:\n%s", s)
	}
	if strings.Contains(s, "/k/zero") || !strings.Contains(s, "\nHost "+sshAlias(targetName)+"\n") {
		t.Fatalf("the earlier block is not replaced:\n%s", s)
	}
}

// Every docker call to an attached host prepares its ssh config, and the
// inventory, its refresh job and a release do so at once. An unchanged
// config is left as it is (the same file, not rewritten), a changed one
// is replaced whole by a rename, and concurrent edits for different hosts
// keep every host's block: an ssh started meanwhile never reads a
// truncated config, which would read an attached host as unreachable while
// releases to it work.
func TestTheSSHConfigIsReplacedWholeAndOnlyWhenItChanges(t *testing.T) {
	t.Parallel()
	a := sshAddress{User: "deploy", Host: "box.example.com", Port: 22}
	cfg := filepath.Join(t.TempDir(), "config")
	if err := upsertSSHStanza(cfg, "box", "/c/socket", sshStanza("box", a, "/k/one", "/c/socket")); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := upsertSSHStanza(cfg, "box", "/c/socket", sshStanza("box", a, "/k/one", "/c/socket")); err != nil {
		t.Fatal(err)
	}
	if after, err := os.Stat(cfg); err != nil || !os.SameFile(before, after) {
		t.Fatalf("an unchanged config was rewritten: %v", err)
	}
	if err := upsertSSHStanza(cfg, "box", "/c/socket", sshStanza("box", a, "/k/two", "/c/socket")); err != nil {
		t.Fatal(err)
	}
	if after, err := os.Stat(cfg); err != nil || os.SameFile(before, after) || after.Mode().Perm() != 0o600 {
		t.Fatalf("a changed config was not replaced whole, private: %v %v", after.Mode(), err)
	}
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("host-%d", i)
			if err := upsertSSHStanza(cfg, name, "/c/socket", sshStanza(name, a, "/k/"+name, "/c/socket")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, _ := os.ReadFile(cfg)
	for i := range 16 {
		if !strings.Contains(string(got), "# BEGIN apptown target "+sshTargetName(fmt.Sprintf("host-%d", i), "/c/socket")+"\n") {
			t.Fatalf("host-%d's block was lost to a concurrent edit:\n%s", i, got)
		}
	}
	if entries, _ := os.ReadDir(filepath.Dir(cfg)); len(entries) != 1 {
		t.Fatalf("leftover files beside the config: %v", entries)
	}
}

// An ssh config that exists but cannot be read is an error, and AppTown's
// block never replaces it: the file's other settings would be lost.
//
// Kills: the failed read taken as an empty file.
func TestAnUnreadableSSHConfigIsNotReplaced(t *testing.T) {
	t.Parallel()
	cfg := filepath.Join(t.TempDir(), "config")
	// A link to itself cannot be read (root included), and renaming over it
	// would succeed, as replacing a real file would.
	if err := os.Symlink(cfg, cfg); err != nil {
		t.Fatal(err)
	}
	a := sshAddress{User: "deploy", Host: "box.example", Port: 22}
	if err := upsertSSHStanza(cfg, "box", "/c/socket", sshStanza("box", a, "/k/one", "/c/socket")); err == nil {
		t.Fatal("an unreadable ssh config was written over")
	}
	if fi, err := os.Lstat(cfg); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the unreadable config was replaced: %v %v", fi, err)
	}
	if err := upsertSSHStanza("", "box", "/c/socket", sshStanza("box", a, "/k/one", "/c/socket")); err == nil {
		t.Fatal("a stanza was written with no ssh config path")
	}
}

// Every SSH consumer uses this block, including Docker's long-lived events
// session. Keepalive probes bound a dead master's lifetime; ControlPersist
// starts its idle timer only after all sessions have closed.
func TestAttachedSSHConnectionSharingOptions(t *testing.T) {
	t.Parallel()
	stanza := sshStanza("box", sshAddress{User: "deploy", Host: "box.example", Port: 22}, "/k/one", "/private/data/ssh/c/socket")
	for _, line := range []string{
		"  ControlMaster auto\n",
		"  ControlPath \"/private/data/ssh/c/socket\"\n",
		"  ControlPersist 3m\n",
		"  ServerAliveInterval 15\n",
		"  ServerAliveCountMax 2\n",
	} {
		if !strings.Contains(stanza, line) {
			t.Fatalf("SSH connection sharing is missing %q:\n%s", line, stanza)
		}
	}
	if got := sshConfigQuote(`/data/a % b/"c`); got != `"/data/a %% b/\"c"` {
		t.Fatalf("SSH path quoting: %s", got)
	}
}

func TestSSHControlSocketsArePrivateAndFollowConnectionIdentity(t *testing.T) {
	t.Parallel()
	// A short private data directory leaves room for OpenSSH's temporary
	// socket suffix even on macOS. t.TempDir includes the long test name.
	dir, err := os.MkdirTemp("/tmp", "at-ssh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	key := filepath.Join(dir, "key")
	if err := os.WriteFile(key, []byte("first key"), 0o600); err != nil {
		t.Fatal(err)
	}
	addr := sshAddress{User: "deploy", Host: "box.example", Port: 22}
	path, err := targetControlPath(dir, "box", addr, key)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, dir+"/ssh/c/") || len(path)+17 >= 104 {
		t.Fatalf("socket must fit under the private data directory: %s", path)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("socket folder is not private: %v %v", info, err)
	}
	if same, err := targetControlPath(dir, "box", addr, key); err != nil || same != path {
		t.Fatalf("calls to one target must reuse its socket: %s %v", same, err)
	}
	for _, change := range []struct {
		name string
		addr sshAddress
	}{
		{"other", addr},
		{"box", sshAddress{User: "other", Host: addr.Host, Port: addr.Port}},
		{"box", sshAddress{User: addr.User, Host: "other.example", Port: addr.Port}},
		{"box", sshAddress{User: addr.User, Host: addr.Host, Port: 2222}},
	} {
		if changed, err := targetControlPath(dir, change.name, change.addr, key); err != nil || changed == path {
			t.Fatalf("changed connection reused a master: %s %v", changed, err)
		}
	}
	otherDir := filepath.Join(dir, "other")
	if other, err := targetControlPath(otherDir, "box", addr, key); err != nil || other == path || sshTargetName("box", other) == sshTargetName("box", path) {
		t.Fatalf("installations share a socket: %s %v", other, err)
	}
	if err := os.WriteFile(key, []byte("replacement key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if changed, err := targetControlPath(dir, "box", addr, key); err != nil || changed == path {
		t.Fatalf("replacement key reused the authenticated master: %s %v", changed, err)
	}
	if _, err := targetControlPath(strings.Repeat("/long", 30), "box", addr, key); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("long socket path must explain why sharing is unavailable: %v", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := targetControlPath(dir, "box", addr, key); err == nil || !strings.Contains(err.Error(), "0700") {
		t.Fatalf("shared socket folder accepted: %v", err)
	}
	if _, err := targetControlPath("", "box", addr, key); err == nil {
		t.Fatal("socket created outside an installation's data directory")
	}
}

// shortHostDataDir omits the test name to leave room for OpenSSH's socket
// suffix. Keep it under TMPDIR: the heavy runner shares only that root with
// the Docker host, so bind mounts must use paths beneath it.
func shortHostDataDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "at-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return dir
}

func TestWritingNamespacedSSHBlockRemovesOnlyItsOldAlias(t *testing.T) {
	t.Parallel()
	cfg := filepath.Join(t.TempDir(), "config")
	old := "# BEGIN apptown target box\nHost apptown-target-box\n  User old\n# END apptown target box\n"
	other := "# BEGIN apptown target other\nHost apptown-target-other\n  User other\n# END apptown target other\n"
	foreign := sshStanza("box", sshAddress{User: "foreign", Host: "foreign.example", Port: 22}, "/foreign/key", "/foreign/socket")
	if err := os.WriteFile(cfg, []byte(old+other+foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	stanza := sshStanza("box", sshAddress{User: "deploy", Host: "box.example", Port: 22}, "/key", "/c/socket")
	if err := upsertSSHStanza(cfg, "box", "/c/socket", stanza); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), old) || !strings.Contains(string(got), other) || !strings.Contains(string(got), foreign) || !strings.HasPrefix(string(got), stanza) {
		t.Fatalf("target update did not remove only its old block:\n%s", got)
	}
}
