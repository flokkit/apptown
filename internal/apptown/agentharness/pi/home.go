package pi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// bakedPiNpmDir is where the agent image's Dockerfile installs pi-subagents
// (HOME=/opt/pi-baked at build time). Sessions symlink it into their
// generated home — dangling on the host, real inside the container.
const bakedPiNpmDir = "/opt/pi-baked/.pi/agent/npm"

// repoSkillsPath: the blessed in-repo skills location (the Agent Skills
// standard's project dir). Listed as an absolute path in GLOBAL settings, so
// skills load without trusting the project — and nothing else in a repo's
// .pi/ or .agents/ gains any authority.
const repoSkillsPath = "/work/.agents/skills"

// Acceptance is pinned EXPLICITLY on both agents. Left on "auto", the
// extension infers contracts that don't fit this roster: a read-only scout
// still owes structured review-findings (a plain answer then reports
// "Background task failed" — complaint #6), and a risky async write demands
// review by a "reviewer" agent that doesn't exist here (builtins are off).
// worker: attested — its report IS the evidence. scout: none, with the
// required reason.
const workerAgentMD = `---
name: worker
description: Carries out a written implementation brief: edits files, writes tests, runs builds and fixes what breaks. It cannot see your conversation — the brief must contain everything it needs.
model: %s
acceptanceRole: writer
acceptance: attested
---

You are a delegated worker inside an AppTown session. Do exactly what the
brief says, completely, and nothing beyond it. When you finish, report
what you changed file by file, what you verified, and anything you could
not do. If the brief is ambiguous, name the ambiguity in your report
instead of guessing silently.
`

const scoutAgentMD = `---
name: scout
description: Fast read-only lookups: find files, trace how something works, summarize code or logs. Never give it a task that changes anything.
model: %s
acceptanceRole: read-only
acceptance:
  level: none
  reason: read-only lookup; the session that sent the task judges the answer directly
---

You are a scout inside an AppTown session. Answer by reading — never modify
anything. Reply with file paths, line references and short quotes, and
say plainly when you cannot find the answer.
`

// DockerConfigDir is relative to the workspace mounted at /work. Pi runs with
// its generated home, so its Docker CLI uses the same client configuration.
const DockerConfigDir = ".apptown/pi-home/.docker"

// preparedHome selects the baked extensions and already-resolved delegate models.
// Project policy and model selection happen before reaching this adapter.
type preparedHome struct {
	SubagentsVersion        string
	Packages                []string
	WorkerModel, ScoutModel string
}

// stageAgentDir writes Pi settings, delegate definitions and the baked package
// symlink. It does not read project state or load extensions from the workspace.
func stageAgentDir(agentDir string, home preparedHome) error {
	settings := map[string]any{
		"quietStartup":        true,
		"defaultProjectTrust": "never",
		"skills":              []string{repoSkillsPath},
	}
	// packages is the whole extension roster for the session: pi-subagents (always
	// on when baked) plus any opt-in extensions this project enabled (extensions.go).
	// Everything here is BAKED in the image; trust:never means a repo can't add to it.
	var packages []string
	if home.SubagentsVersion != "" {
		packages = append(packages, "npm:pi-subagents@"+home.SubagentsVersion)
	}
	packages = append(packages, home.Packages...)
	if len(packages) > 0 {
		settings["packages"] = packages
	}
	if home.SubagentsVersion != "" {
		// disableBuiltins: the roster below is the whole roster — six stock
		// agents inheriting the session model would put the expensive model
		// everywhere and give the operator names nobody here defined.
		settings["subagents"] = map[string]any{
			"defaultModel":    home.WorkerModel,
			"disableBuiltins": true,
		}
		npmLink := filepath.Join(agentDir, "npm")
		_ = os.Remove(npmLink)
		if err := os.Symlink(bakedPiNpmDir, npmLink); err != nil {
			return err
		}
		agentsDir := filepath.Join(agentDir, "agents")
		if err := os.MkdirAll(agentsDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(agentsDir, "worker.md"),
			[]byte(fmt.Sprintf(workerAgentMD, home.WorkerModel)), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(agentsDir, "scout.md"),
			[]byte(fmt.Sprintf(scoutAgentMD, home.ScoutModel)), 0o644); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(agentDir, "settings.json"), append(b, '\n'), 0o644)
}
