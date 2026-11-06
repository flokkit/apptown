package diskspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestCategorySizesAreMeasuredAndFailuresStayUnknown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	at := time.Now()
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("seven!!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(os.TempDir(), filepath.Join(dir, "outside")); err != nil {
		t.Fatal(err)
	}
	c := Folder(t.Context(), "Workspaces", dir, at)
	if c.Bytes != 7 || c.Unknown != "" || c.ReadAt != at {
		t.Fatalf("folder: %+v", c)
	}
	if c := Folder(t.Context(), "Backups", filepath.Join(dir, "absent"), at); c.Unknown != "" || c.Bytes != 0 {
		t.Fatalf("absent: %+v", c)
	}
	if c := Folder(t.Context(), "Workspaces", filepath.Join(dir, "file", "bad"), at); c.Unknown == "" {
		t.Fatalf("failed: %+v", c)
	}
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if !reflect.DeepEqual(args, []string{"system", "df", "--format", "{{json .}}"}) {
			t.Fatalf("unexpected command: %v", args)
		}
		return []byte("{\"Type\":\"Images\",\"Size\":\"1.25GB\"}\n{\"Type\":\"Local Volumes\",\"Size\":\"2kB\"}\n{\"Type\":\"Build Cache\",\"Size\":\"0B\"}"), nil
	}
	got := DockerCategories(t.Context(), run, at)
	if got[0].Bytes != 2000 || got[1].Bytes != 1250000000 || got[2].Bytes != 0 {
		t.Fatalf("Docker: %+v", got)
	}
	for _, c := range got {
		if c.Unknown != "" || c.ReadAt != at {
			t.Fatalf("Docker: %+v", got)
		}
	}
	for _, raw := range []string{"", `{"Type":"Images","Size":"nonsense"}`} {
		got := DockerCategories(t.Context(), func(context.Context, ...string) ([]byte, error) { return []byte(raw), nil }, at)
		for _, c := range got {
			if c.Unknown == "" {
				t.Fatalf("invalid read became empty: %+v", got)
			}
		}
	}
	got = DockerCategories(t.Context(), func(context.Context, ...string) ([]byte, error) { return nil, errors.New("secret") }, at)
	if got[0].Unknown == "" {
		t.Fatal("failed read became zero")
	}
}
