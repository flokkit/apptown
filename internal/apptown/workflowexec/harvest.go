package workflowexec

import (
	"errors"
	"io/fs"
	"os"
	"path"
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
	// MdFile is the file the output came from — the declared output file,
	// or the single markdown file that stood in for an undeclared one ("" when
	// neither) — attached as the typed output artifact, not again as a plain
	// file.
	MdFile string
	// MissingOutput names the declared output file when the agent did not
	// write it or left it empty: the step's output is missing, and nothing
	// else stands in for it.
	MissingOutput string
	// Problem says why what the agent wrote could not be read: the step is
	// an error, never judged on part of its work.
	Problem string
}

// useDeclared makes the declared output file the output, and nothing else:
// absent or blank, the output is missing (fallback audit F16).
func (h *workspaceHarvest) useDeclared(file, body string) {
	h.Output, h.MdFile, h.MissingOutput = body, path.Clean(file), ""
	if strings.TrimSpace(body) == "" {
		h.Output, h.MissingOutput = "", file
	}
}

// harvestWorkspace collects files the agent wrote/changed since start
// (agents prefer their write tool over stdout; the files ARE the outcome)
// and picks the output body: the declared output file when the step
// declares one, and only it; else a single markdown file the agent wrote,
// else the joined assistant text. What cannot be read is the harvest's
// Problem.
func harvestWorkspace(ws string, start time.Time, finalText, outputFile string) workspaceHarvest {
	var wrote []wroteFile
	walkErr := filepath.Walk(ws, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(ws, path)
		if info.IsDir() {
			if rel == "." {
				return nil
			}
			// Skip AppTown's own injected dirs and hidden dirs — but NOT .apptown
			// itself: agents sometimes write outputs there.
			if rel == filepath.Join(".apptown", "resources") || rel == filepath.Join(".apptown", "sessions") ||
				rel == filepath.Join(".apptown", "pi-home") {
				return filepath.SkipDir
			}
			if strings.HasPrefix(info.Name(), ".") && info.Name() != ".apptown" {
				return filepath.SkipDir
			}
			// Dependency/build output dirs are not authored outcomes.
			switch info.Name() {
			case "node_modules", "vendor", "venv", "__pycache__", "dist", "build", "target":
				return filepath.SkipDir
			}
			return nil
		}
		if rel == filepath.Join(".apptown", "steering.md") || strings.HasPrefix(filepath.Base(rel), ".") {
			return nil
		}
		// AppTown-generated runtime config (ambient build env) is re-staged at
		// step start — never an authored outcome.
		if strings.HasPrefix(rel, filepath.Join(".apptown", "ambient")+string(filepath.Separator)) {
			return nil
		}
		if info.ModTime().Before(start) || info.Size() > 256*1024 || len(wrote) >= 40 {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		wrote = append(wrote, wroteFile{rel, string(b)})
		return nil
	})
	if walkErr != nil {
		return workspaceHarvest{Problem: "the step's written files could not be read: " + walkErr.Error()}
	}

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
	// A declared output file is the output — read it regardless of mtime
	// (agents may overwrite a pre-existing report).
	if outputFile != "" {
		b, err := os.ReadFile(filepath.Join(ws, outputFile))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			h.useDeclared(outputFile, "")
		case err != nil:
			h.Problem = "the output file could not be read: " + err.Error()
		default:
			h.useDeclared(outputFile, string(b))
		}
	}
	return h
}

// --- record ----------------------------------------------------------------

// agentRecord is everything recordAgentOutcome needs to close a step.
