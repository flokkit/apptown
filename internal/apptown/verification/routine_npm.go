package verification

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/routinechange"
)

//go:embed scripts/npm-observe.cjs
var npmObserver []byte

type npmSide struct {
	Input     []string          `json:"input"`
	Output    []string          `json:"output"`
	Installed map[string]string `json:"installed"`
}
type npmObservation struct {
	Node     string  `json:"node"`
	NPM      string  `json:"npm"`
	Platform string  `json:"platform"`
	Before   npmSide `json:"before"`
	After    npmSide `json:"after"`
}
type npmEvidence struct {
	Image        string `json:"image"`
	Node         string `json:"node"`
	NPM          string `json:"npm"`
	Platform     string `json:"platform"`
	ReportDigest string `json:"reportDigest"`
	// Worker scratch removal is asynchronous to its response and sweep-retried.
	// Container settlement is required; this field never claims scratch removal.
	ScratchSettlement string `json:"scratchSettlement"`
}

func validateNPM(ctx context.Context, exec Executor, dataDir string, before, after routinechange.NPMSnapshot, candidate routinechange.NPMCandidate) (evidence npmEvidence, err error) {
	if exec.Run == nil || exec.Image == nil || exec.AgentImage == "" {
		return evidence, errors.New("dependency validation executor is unavailable")
	}
	image := exec.Image(ctx, execution.ImageRequest{Tag: exec.AgentImage})
	if image.Err != "" || !strings.HasPrefix(image.ID, "sha256:") || len(image.ID) != 71 {
		return evidence, fmt.Errorf("dependency validator image cannot be observed: %s", image.Err)
	}
	if _, err = hex.DecodeString(strings.TrimPrefix(image.ID, "sha256:")); err != nil {
		return evidence, errors.New("dependency validator image identity is malformed")
	}
	staging := filepath.Join(dataDir, "staging")
	if err = os.MkdirAll(staging, 0o700); err != nil {
		return evidence, err
	}
	dir, err := os.MkdirTemp(staging, "routine-npm-")
	if err != nil {
		return evidence, err
	}
	defer func() {
		if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("removing validator staging: %w", cleanupErr))
		}
	}()
	for i, side := range []string{"before", "after"} {
		s := before
		if i == 1 {
			s = after
		}
		folder := filepath.Join(dir, side)
		if err = os.Mkdir(folder, 0o700); err != nil {
			return evidence, err
		}
		if err = os.WriteFile(filepath.Join(folder, "package.json"), s.Manifest, 0o600); err != nil {
			return evidence, err
		}
		if err = os.WriteFile(filepath.Join(folder, "package-lock.json"), s.Lock, 0o600); err != nil {
			return evidence, err
		}
	}
	out := exec.Run(ctx, execution.Request{Name: "apptown-" + filepath.Base(dir), Workspace: dir, Image: image.ID, Argv: []string{"node", "/routine-observer.cjs"}, NoEntrypoint: true, Timeout: 7 * time.Minute, Report: "observation.json", Files: []execution.File{{Path: "/routine-observer.cjs", Content: npmObserver, Mode: 0o444}}, Limits: execution.DefaultTaskLimits})
	if !out.Exited0() || out.RunErr != nil || out.ReportErr != nil {
		return evidence, fmt.Errorf("dependency validation did not complete: termination=%s exit=%d: %w", out.Termination, out.ExitCode, errors.Join(out.RunErr, out.ReportErr, out.CleanupErr))
	}
	var report npmObservation
	if err = json.Unmarshal(out.Report, &report); err != nil {
		return evidence, fmt.Errorf("reading dependency observation: %w", err)
	}
	if report.Node == "" || report.NPM == "" || report.Platform == "" {
		return evidence, errors.New("dependency validator did not observe its tools and platform")
	}
	for i, side := range []npmSide{report.Before, report.After} {
		s, expected := before, candidate.Before
		if i == 1 {
			s, expected = after, candidate.After
		}
		hashes := []string{digestNPM(s.Manifest), digestNPM(s.Lock)}
		if len(side.Input) != 2 || len(side.Output) != 2 || side.Input[0] != hashes[0] || side.Input[1] != hashes[1] || side.Output[0] != hashes[0] || side.Output[1] != hashes[1] {
			return evidence, errors.New("dependency validator observed different input bytes")
		}
		installed := make(map[string]json.RawMessage, len(side.Installed))
		for path, raw := range side.Installed {
			installed[path] = json.RawMessage(raw)
		}
		if err = routinechange.VerifyInstalled(s, expected, installed); err != nil {
			return evidence, err
		}
	}
	scratch := "local staging removal required before return"
	if !exec.Local {
		scratch = "worker scratch removal is unobserved here and retried by its sweep"
	}
	return npmEvidence{Image: image.ID, Node: report.Node, NPM: report.NPM, Platform: report.Platform, ReportDigest: digestNPM(out.Report), ScratchSettlement: scratch}, nil
}
func digestNPM(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
