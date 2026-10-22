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
	// A block an earlier release wrote, under the alias it used then.
	before1 := "# BEGIN forge target box\nHost forge-target-box\n  IdentityFile /k/zero\n# END forge target box\n"
	if err := os.WriteFile(cfg, []byte("Host mine\n  User me\n\n"+before1), 0o600); err != nil {
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
	if strings.Count(s, "BEGIN forge target box") != 1 || !strings.Contains(s, "IdentityFile /k/two") || strings.Contains(s, "/k/one") || !strings.HasPrefix(s, "Host mine\n  User me\n") {
		t.Fatalf("AppTown replaces exactly its own block and keeps the rest:\n%s", s)
	}
	if strings.Contains(s, "forge-target-box") || strings.Contains(s, "/k/zero") || !strings.Contains(s, "\nHost apptown-target-box\n") {
		t.Fatalf("the earlier release's block is not replaced by one under the current alias:\n%s", s)
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
		if !strings.Contains(string(got), fmt.Sprintf("# BEGIN forge target host-%d\n", i)) {
			t.Fatalf("host-%d's block was lost to a concurrent edit:\n%s", i, got)
		}
	}
	if entries, _ := os.ReadDir(filepath.Dir(cfg)); len(entries) != 1 {
		t.Fatalf("leftover files beside the config: %v", entries)
	}
}
