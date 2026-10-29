package docker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

// The contract at a commit is read one way for every reader: the regular
// file at .apptown/app.yaml, at most MaxContractBytes. A contract only at
// .forge/app.yaml (the folder before the AppTown rename) is no contract; a
// link, a folder in its place or an oversized file is an error, never
// "no contract" and never read.
func TestTheContractAtACommitIsReadOneWay(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	gittest.RunAsTester(t, repo, "init", "-q", "-b", "main")
	commit := func(setup func()) string {
		t.Helper()
		if err := os.RemoveAll(filepath.Join(repo, ".apptown")); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(repo, ".forge")); err != nil {
			t.Fatal(err)
		}
		setup()
		gittest.RunAsTester(t, repo, "add", "-f", "-A")
		gittest.RunAsTester(t, repo, "commit", "-q", "--allow-empty", "-m", "c")
		return gittest.RunAsTester(t, repo, "rev-parse", "HEAD")
	}
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, rel string) {
		t.Helper()
		if err := os.Symlink(target, filepath.Join(repo, rel)); err != nil {
			t.Fatal(err)
		}
	}
	const body = "app:\n  web:\n    image: new:v1\n    public: true\n    ports: [\"8080\"]\n"
	good := commit(func() { write(ContractPath, body) })
	forgeOnly := commit(func() { write(".forge/app.yaml", body) })
	none := commit(func() {})
	oversized := commit(func() { write(ContractPath, "# "+strings.Repeat("x", MaxContractBytes)+"\n") })
	linkedFile := commit(func() {
		write("elsewhere.yaml", body)
		write(".apptown/keep", "")
		link("../elsewhere.yaml", ContractPath)
	})
	linkedFolder := commit(func() { write("other/app.yaml", body); link("other", ".apptown") })
	folderInPlace := commit(func() { write(ContractPath+"/inner", body) })

	if raw, err := ContractFileAt(t.Context(), repo, good); err != nil || string(raw) != body {
		t.Fatalf("the contract: %q %v", raw, err)
	}
	for name, sha := range map[string]string{"only .forge/app.yaml": forgeOnly, "none": none} {
		if _, err := ContractFileAt(t.Context(), repo, sha); !errors.Is(err, ErrNoContract) {
			t.Errorf("%s: %v, want ErrNoContract", name, err)
		}
	}
	for name, c := range map[string]struct{ sha, says string }{
		"oversized":       {oversized, "over"},
		"a linked file":   {linkedFile, "is a link"},
		"a linked folder": {linkedFolder, "is a link"},
		"a folder":        {folderInPlace, "not a regular file"},
	} {
		_, err := ContractFileAt(t.Context(), repo, c.sha)
		if err == nil || errors.Is(err, ErrNoContract) || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v, want an error saying %q", name, err, c.says)
		}
	}

	folder := t.TempDir()
	if err := os.MkdirAll(filepath.Join(folder, ".forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, ".forge", "app.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSelectedContract(folder); !errors.Is(err, ErrNoContract) {
		t.Fatalf("a selected tree with only .forge/app.yaml: %v, want ErrNoContract", err)
	}
}
