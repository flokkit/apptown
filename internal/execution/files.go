package execution

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// File is a file the forge generated for a container (a registry client's
// configuration; design/operations/workers.md):
// its bytes, presented read-only at Path. It travels beside a request's
// tree, never in it: whoever runs the container writes it into a folder of
// its own and mounts it, so nothing in a project's tree can choose where it
// is written.
type File struct {
	Path    string      `json:"path"` // absolute and clean, in the container
	Content []byte      `json:"content"`
	Mode    fs.FileMode `json:"mode"` // permission bits only
}

// Bounds on what a request's files may be: they are small generated
// configuration, never a way to send a tree.
const (
	MaxFiles     = 32
	MaxFileBytes = 1 << 20 // all of a request's files together
)

// ValidFiles says whether files may be presented to a container: each at an
// absolute, clean path of its own, outside the tree (/work) and history
// (/history), with permission bits only, within the bounds.
func ValidFiles(files []File) error {
	if len(files) > MaxFiles {
		return fmt.Errorf("%d generated files, over the %d limit", len(files), MaxFiles)
	}
	seen := map[string]bool{}
	total := 0
	for _, f := range files {
		p := f.Path
		if !path.IsAbs(p) || path.Clean(p) != p || p == "/" || strings.ContainsAny(p, ":,\x00") {
			return fmt.Errorf("a generated file's path must be absolute and clean: %q", p)
		}
		for _, reserved := range []string{"/work", "/history"} {
			if p == reserved || strings.HasPrefix(p, reserved+"/") {
				return fmt.Errorf("a generated file cannot be placed in %s: %q", reserved, p)
			}
		}
		if seen[p] {
			return fmt.Errorf("two generated files at %q", p)
		}
		seen[p] = true
		if f.Mode&^fs.ModePerm != 0 || f.Mode == 0 {
			return fmt.Errorf("a generated file's mode is permission bits only, and some: %q has %v", p, f.Mode)
		}
		total += len(f.Content)
	}
	if total > MaxFileBytes {
		return fmt.Errorf("the generated files are %d bytes, over the %d limit", total, MaxFileBytes)
	}
	return nil
}

// FileIdentity is what identifies a generated file in a request's identity:
// its path, its mode and a digest of its bytes.
type FileIdentity struct {
	Path   string      `json:"path"`
	Mode   fs.FileMode `json:"mode"`
	SHA256 string      `json:"sha256"`
}

// IdentifyFiles is the ordered identity of files: a request whose files
// change, at the same paths or not, is another request.
func IdentifyFiles(files []File) []FileIdentity {
	var out []FileIdentity
	for _, f := range files {
		sum := sha256.Sum256(f.Content)
		out = append(out, FileIdentity{Path: f.Path, Mode: f.Mode, SHA256: hex.EncodeToString(sum[:])})
	}
	return out
}

// StageFiles writes files into dir, a folder the caller owns and that does
// not exist yet, and answers the Docker mounts presenting each read-only at
// its path. A dir that already exists is used only if it holds exactly these
// files (a task ensured again): files a container may already have mounted
// are never rewritten, and other files there are ErrTaskConflict.
func StageFiles(dir string, files []File) ([]string, error) {
	if err := ValidFiles(files); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}
	if _, err := os.Lstat(dir); err == nil {
		if err := sameFiles(dir, files); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrTaskConflict, err)
		}
		return fileMounts(dir, files), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	// Written whole under another name, then renamed into place: a dir that
	// exists is always complete.
	tmp, err := os.MkdirTemp(parent, filepath.Base(dir)+".*.tmp")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	for i, f := range files {
		if err := os.WriteFile(filepath.Join(tmp, strconv.Itoa(i)), f.Content, f.Mode); err != nil {
			os.RemoveAll(tmp)
			return nil, err
		}
		// The umask never narrows what the container reads.
		if err := os.Chmod(filepath.Join(tmp, strconv.Itoa(i)), f.Mode); err != nil {
			os.RemoveAll(tmp)
			return nil, err
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	return fileMounts(dir, files), nil
}

func fileMounts(dir string, files []File) []string {
	var mounts []string
	for i, f := range files {
		mounts = append(mounts, filepath.Join(dir, strconv.Itoa(i))+":"+f.Path+":ro")
	}
	return mounts
}

// sameFiles says whether dir holds exactly files, as StageFiles wrote them.
func sameFiles(dir string, files []File) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != len(files) {
		return fmt.Errorf("%s holds %d files, not the %d asked for", dir, len(entries), len(files))
	}
	for i, f := range files {
		name := filepath.Join(dir, strconv.Itoa(i))
		fi, err := os.Lstat(name)
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != f.Mode {
			return fmt.Errorf("%s is not the file asked for at %s", name, f.Path)
		}
		body, err := os.ReadFile(name)
		if err != nil || !bytes.Equal(body, f.Content) {
			return fmt.Errorf("%s holds other bytes than asked for at %s", name, f.Path)
		}
	}
	return nil
}
