package scm

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/gitexec/gittest"
)

// Every publication checks what it brings into the repository, whoever made
// it: a change to .forge but its native contract, a new file over 10 MiB,
// or new files over 50 MiB in all is refused, the branch does not move, and
// the work stays where it was made. The same files within the limits, and
// the contract, are published.
func TestAPublicationIsCheckedForWhatItBringsIn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		files   map[string]int // path -> size in bytes
		refused string
	}{
		{"a runtime file under .forge", map[string]int{".forge/runtime.json": 10}, ".forge/runtime.json"},
		{"a file over the limit", map[string]int{"dump.bin": maxPublishedFile + 1}, "dump.bin"},
		{"too much in all", map[string]int{"a.bin": 9 << 20, "b.bin": 9 << 20, "c.bin": 9 << 20, "d.bin": 9 << 20, "e.bin": 9 << 20, "f.bin": 9 << 20}, "over the 50 MiB"},
		{"source and the contract", map[string]int{"main.go": 10, ".forge/app.yaml": 10}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newPublicationFixture(t)
			writer := f.writer("writer")
			for name, size := range tc.files {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(writer, name)), 0o755); err != nil {
					t.Fatal(err)
				}
				body := make([]byte, size)
				for i := range body {
					body[i] = byte(i*7 + int(name[0])) // distinct per file, so each is its own blob
				}
				if err := os.WriteFile(filepath.Join(writer, name), body, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			gittest.RunAsTester(t, writer, "add", "-A", "-f")
			gittest.RunAsTester(t, writer, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", tc.name)
			before := f.tip()
			_, err := f.publish(writer, Grant{Generation: f.authority().Generation})
			if tc.refused == "" {
				if err != nil || f.tip() == before {
					t.Fatalf("source was not published: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrNotPublished) || !strings.Contains(err.Error(), tc.refused) {
				t.Fatalf("the publication: %v", err)
			}
			if f.tip() != before {
				t.Fatal("a refused publication moved the branch")
			}
		})
	}
}
