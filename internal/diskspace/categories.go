package diskspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Category is measured use, or an explicit failure to measure it. Categories
// can overlap (a database can live in a volume); they are never a disk total.
type Category struct {
	Name    string    `json:"name"`
	Bytes   int64     `json:"bytes"`
	Unknown string    `json:"unknown,omitempty"`
	ReadAt  time.Time `json:"readAt"`
}

// Folder measures regular files without following symlinks outside the folder.
// An absent folder has no files; a failed traversal is unknown, never partial use.
func Folder(ctx context.Context, name, path string, at time.Time) Category {
	c := Category{Name: name, ReadAt: at}
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if p == path && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			c.Bytes += info.Size()
		}
		return nil
	})
	if err != nil {
		c.Bytes = 0
		c.Unknown = "The folder could not be measured."
	}
	return c
}

// DockerCategories reads Docker's daemon-wide summary, including untagged
// resources. It never selects resources for removal. Docker rounds these sizes.
func DockerCategories(ctx context.Context, run func(context.Context, ...string) ([]byte, error), at time.Time) []Category {
	names := []string{"Volumes", "Images", "Build cache"}
	out := make([]Category, len(names))
	for i, name := range names {
		out[i] = Category{Name: name, ReadAt: at, Unknown: "Docker use could not be measured."}
	}
	raw, err := run(ctx, "system", "df", "--format", "{{json .}}")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var row struct{ Type, Size string }
		if json.Unmarshal([]byte(line), &row) != nil {
			return out
		}
		name := map[string]string{"Local Volumes": "Volumes", "Images": "Images", "Build Cache": "Build cache"}[row.Type]
		for i := range out {
			if out[i].Name == name {
				n, err := dockerBytes(row.Size)
				if err == nil {
					out[i].Bytes, out[i].Unknown = n, ""
				}
			}
		}
	}
	return out
}
func dockerBytes(s string) (int64, error) {
	for i, r := range s {
		if r != '.' && (r < '0' || r > '9') {
			n, err := strconv.ParseFloat(s[:i], 64)
			scale, ok := map[string]float64{"B": 1, "kB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "PB": 1e15}[strings.TrimSpace(s[i:])]
			if err != nil || !ok || n < 0 {
				break
			}
			return int64(n * scale), nil
		}
	}
	return 0, fmt.Errorf("invalid Docker size %q", s)
}
