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
	a, err := ParseSSHAddress("ssh://deploy@droplet.example.com:2222")
	if err != nil || a.User != "deploy" || a.Host != "droplet.example.com" || a.Port != 2222 {
		t.Fatalf("parse: %+v %v", a, err)
	}
	if a, err := ParseSSHAddress("ssh://root@10.0.0.5"); err != nil || a.Port != 22 {
		t.Fatalf("default port: %+v %v", a, err)
	}
	for _, bad := range []string{"tcp://x:2376", "root@host", "ssh://host", "ssh://a@b:70000", "ssh://a@b; rm -rf /"} {
		if _, err := ParseSSHAddress(bad); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	// A block AppTown wrote before, with another key.
	earlier := "# BEGIN apptown target box\nHost apptown-target-box\n  IdentityFile /k/zero\n# END apptown target box\n"
	if err := os.WriteFile(cfg, []byte("Host mine\n  User me\n\n"+earlier), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := upsertSSHStanza(cfg, "box", sshStanza("box", a, "/k/one")); err != nil {
		t.Fatal(err)
	}
	if err := upsertSSHStanza(cfg, "box", sshStanza("box", a, "/k/two")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(cfg)
	s := string(got)
	if strings.Count(s, "BEGIN apptown target box") != 1 || !strings.Contains(s, "IdentityFile /k/two") || strings.Contains(s, "/k/one") || !strings.HasPrefix(s, "Host mine\n  User me\n") {
		t.Fatalf("AppTown replaces exactly its own block and keeps the rest:\n%s", s)
	}
	if strings.Contains(s, "/k/zero") || !strings.Contains(s, "\nHost apptown-target-box\n") {
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
	a := SSHAddress{User: "deploy", Host: "box.example.com", Port: 22}
	cfg := filepath.Join(t.TempDir(), "config")
	if err := upsertSSHStanza(cfg, "box", sshStanza("box", a, "/k/one")); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := upsertSSHStanza(cfg, "box", sshStanza("box", a, "/k/one")); err != nil {
		t.Fatal(err)
	}
	if after, err := os.Stat(cfg); err != nil || !os.SameFile(before, after) {
		t.Fatalf("an unchanged config was rewritten: %v", err)
	}
	if err := upsertSSHStanza(cfg, "box", sshStanza("box", a, "/k/two")); err != nil {
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
			if err := upsertSSHStanza(cfg, name, sshStanza(name, a, "/k/"+name)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, _ := os.ReadFile(cfg)
	for i := range 16 {
		if !strings.Contains(string(got), fmt.Sprintf("# BEGIN apptown target host-%d\n", i)) {
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
	a := SSHAddress{User: "deploy", Host: "box.example", Port: 22}
	if err := upsertSSHStanza(cfg, "box", sshStanza("box", a, "/k/one")); err == nil {
		t.Fatal("an unreadable ssh config was written over")
	}
	if fi, err := os.Lstat(cfg); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the unreadable config was replaced: %v %v", fi, err)
	}
	if err := upsertSSHStanza("", "box", sshStanza("box", a, "/k/one")); err == nil {
		t.Fatal("a stanza was written with no ssh config path")
	}
}
