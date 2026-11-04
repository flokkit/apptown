package check

import (
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// sourceFolders requires resolved contract context even for repository rubrics:
// source belongs to declared parts, never an inferred repository-wide default.
func sourceFolders(c Declaration, src Source) ([]string, error) {
	if src.PartsErr != nil {
		return nil, fmt.Errorf("sourceFiles: the app contract could not be read: %w", src.PartsErr)
	}
	if src.NoContract || len(src.Parts) == 0 {
		return nil, fmt.Errorf("sourceFiles: no declared part context; provide .apptown/app.yaml")
	}
	var folders []string
	for _, p := range src.Parts {
		if !(len(c.AppliesTo) == 1 && c.AppliesTo[0] == AppliesRepository) && !matches(p, c.AppliesTo) {
			continue
		}
		// Pulled images have no source folder.
		if p.Folder == "" {
			continue
		}
		f := path.Clean(p.Folder)
		if f != "." && !fs.ValidPath(f) {
			return nil, fmt.Errorf("sourceFiles: invalid part folder %q", p.Folder)
		}
		folders = append(folders, f)
	}
	return folders, nil
}

func withinFolders(rel string, folders []string) bool {
	for _, f := range folders {
		f = path.Clean(f)
		if f == "." || rel == f || strings.HasPrefix(rel, f+"/") {
			return true
		}
	}
	return false
}
