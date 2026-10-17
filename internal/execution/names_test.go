package execution

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// Runs started in the same instant (a fleet check runs one per project at
// once) never share a container name.
func TestUniqueNamesDoNotCollide(t *testing.T) {
	t.Parallel()
	const n = 2000
	names := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { names[i] = UniqueName("forge-exec-image") })
	}
	wg.Wait()
	seen := map[string]bool{}
	valid := regexp.MustCompile(`^forge-exec-image-[0-9a-f]+$`)
	for _, name := range names {
		if seen[name] || !valid.MatchString(name) {
			t.Fatalf("name %q repeated or not a container name", name)
		}
		seen[name] = true
	}
}

// No container is named from the clock: a time-derived suffix repeats for
// runs started together.
func TestNoContainerIsNamedFromTheClock(t *testing.T) {
	t.Parallel()
	clock := regexp.MustCompile(`UnixNano\(\)\s*%`)
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			if clock.MatchString(line) {
				t.Errorf("%s:%d names something from the clock; use execution.UniqueName", path, i+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
