package pi

import (
	"context"
	"flokkit.io/forge/internal/ids"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"flokkit.io/forge/internal/console"
)

// ConsoleFiles materializes the same generated Pi home for persistent consoles.
func (home Preparer) ConsoleFiles(ctx context.Context, dataDir string, projectID ids.ID) (map[string]console.Staged, error) {
	dir, err := os.MkdirTemp("", "forge-pi-home-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	piAgent := filepath.Join(dir, ".pi", "agent")
	if err := os.MkdirAll(piAgent, 0o755); err != nil {
		return nil, err
	}
	models, err := home.models.ModelsJSON(ctx)
	if err != nil {
		return nil, fmt.Errorf("generating pi config: %w", err)
	}
	if err := os.WriteFile(filepath.Join(piAgent, "models.json"), models, 0o600); err != nil {
		return nil, err
	}
	if err := home.StageAgentDir(ctx, piAgent, projectID); err != nil {
		return nil, err
	}
	// Stage the fd binary pi would otherwise download on first use.
	if fd, err := os.ReadFile(filepath.Join(dataDir, "tools", "fd-linux-"+runtime.GOARCH)); err == nil {
		binDir := filepath.Join(piAgent, "bin")
		_ = os.MkdirAll(binDir, 0o755)
		_ = os.WriteFile(filepath.Join(binDir, "fd"), fd, 0o755)
	}
	files, err := stagedUnder(dir, "pi-home")
	if err != nil {
		return nil, err
	}
	return files, nil
}

// stagedUnder reads a staged directory into the files a provision places,
// rooted at prefix inside the session dir; symbolic links stay links.
func stagedUnder(dir, prefix string) (map[string]console.Staged, error) {
	files := map[string]console.Staged{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if rel == "." {
			return nil
		}
		key := filepath.Join(prefix, rel)
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			files[key] = console.Staged{Link: target}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[key] = console.Staged{Data: data, Mode: info.Mode().Perm()}
		return nil
	})
	return files, err
}
