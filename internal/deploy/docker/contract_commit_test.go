package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

const commitContractFixture = `app:
  web:
    image: reg.example/app:1
    ports: ["8080"]
    public: true
init:
  - name: migrate
    image: reg.example/app:1
needs:
  postgres: {}
`

// A look at a past release reads the contract AT THE COMMIT through the
// same native reader as every checkout. Retired forms are not interpreted.
func TestReadAppContractAtCommit(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := gitForDockerTest(repo, append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return out
	}
	git("init", "-q")
	if err := os.MkdirAll(filepath.Join(repo, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(repo, ".apptown", "app.json"), []byte(`{"image":"reg/old@sha256:1","port":9000}`), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "app.json era")
	old := git("rev-parse", "HEAD")
	_ = os.Remove(filepath.Join(repo, ".apptown", "app.json"))
	_ = os.WriteFile(filepath.Join(repo, ".apptown", "app.yaml"), []byte(commitContractFixture), 0o644)
	git("add", "-A")
	git("commit", "-q", "-m", "app.yaml era")

	c, err := ReadContractAt(context.Background(), repo, "HEAD")
	if err != nil || c.PublicPart().Image != "reg.example/app:1" || len(c.Needs) != 1 || len(c.Init) != 1 {
		t.Fatalf("HEAD: %+v %v", c, err)
	}
	if _, err := ReadContractAt(context.Background(), repo, old); err != ErrNoContract {
		t.Fatalf("retired app.json must not supply a contract: %v", err)
	}
	if _, err := ReadContractAt(context.Background(), repo, "HEAD~2"); err == nil {
		t.Fatal("a commit without a contract must fail to read")
	}
	// The checkout reader and the commit reader are the same reader.
	fromTree, _ := ReadWorkspaceContract(repo)
	if fromTree == nil || fromTree.PublicPart().Image != c.PublicPart().Image {
		t.Fatalf("checkout read: %+v", fromTree)
	}
}
