package workflowexec

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// --- harvest ---------------------------------------------------------------

type wroteFile struct {
	rel  string
	body string
}

// workspaceHarvest is what the agent left in the workspace: the files it
// wrote or changed, and the body that becomes its output artifact.
type workspaceHarvest struct {
	Files  []wroteFile
	Output string
	// MdFile is the single markdown file that stood in for the output (""
	// when there was not exactly one) — it is attached as the typed output
	// artifact, not again as a plain file.
	MdFile string
}

// harvestWorkspace collects files the agent wrote/changed since start
// (agents prefer their write tool over stdout; the files ARE the outcome)
// and picks the output body: an explicitly declared output file, else a
// single markdown file the agent wrote, else the joined assistant text.
func harvestWorkspace(ws string, start time.Time, finalText, outputFile string) workspaceHarvest {
	var wrote []wroteFile
	_ = filepath.Walk(ws, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(ws, path)
		if info.IsDir() {
			if rel == "." {
				return nil
			}
			// Skip forge's own injected dirs and hidden dirs — but NOT .forge
			// itself: agents sometimes write outputs there.
			if rel == filepath.Join(".forge", "resources") || rel == filepath.Join(".forge", "sessions") ||
				rel == filepath.Join(".forge", "pi-home") {
				return filepath.SkipDir
			}
			if strings.HasPrefix(info.Name(), ".") && info.Name() != ".forge" {
				return filepath.SkipDir
			}
			// Dependency/build output dirs are not authored outcomes.
			switch info.Name() {
			case "node_modules", "vendor", "venv", "__pycache__", "dist", "build", "target":
				return filepath.SkipDir
			}
			return nil
		}
		if rel == filepath.Join(".forge", "steering.md") || strings.HasPrefix(filepath.Base(rel), ".") {
			return nil
		}
		// Forge-generated runtime config (ambient build env) is re-staged at
		// step start — never an authored outcome.
		if strings.HasPrefix(rel, filepath.Join(".forge", "ambient")+string(filepath.Separator)) {
			return nil
		}
		if info.ModTime().Before(start) || info.Size() > 256*1024 || len(wrote) >= 40 {
			return nil
		}
		if b, err := os.ReadFile(path); err == nil {
			wrote = append(wrote, wroteFile{rel, string(b)})
		}
		return nil
	})

	// The output artifact body: a single markdown file the agent wrote beats
	// its chat text; otherwise the joined assistant text.
	h := workspaceHarvest{Files: wrote, Output: finalText}
	var mdFiles []wroteFile
	for _, f := range wrote {
		if strings.HasSuffix(f.rel, ".md") {
			mdFiles = append(mdFiles, f)
		}
	}
	if len(mdFiles) == 1 {
		h.Output, h.MdFile = mdFiles[0].body, mdFiles[0].rel
	}
	// An explicitly declared output file is authoritative — read it regardless
	// of mtime (agents may overwrite a pre-existing report).
	if outputFile != "" {
		if b, err := os.ReadFile(filepath.Join(ws, outputFile)); err == nil && len(strings.TrimSpace(string(b))) > 0 {
			h.Output = string(b)
		}
	}
	return h
}

// --- record ----------------------------------------------------------------

// agentRecord is everything recordAgentOutcome needs to close a step.
