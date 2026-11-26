package workflowexec

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func TestAgentPromptKeepsSendBackFeedbackAcrossRetryModes(t *testing.T) {
	t.Parallel()
	a := &Executor{}
	for _, mode := range []string{"continue", ""} {
		cfg := agentConfig{Prompt: "do it", RetryMode: mode, Sendback: "keep my feedback"}
		prompt := a.agentPrompt(t.Context(), &workflow.Run{}, &cfg, workflow.Source{}, nil)
		if !strings.Contains(prompt, "keep my feedback") || mode == "" && !strings.Contains(prompt, "do it") {
			t.Fatalf("%s: lost task or feedback: %q", mode, prompt)
		}
	}
}

func TestContinuedAgentNamesOnlyItsDeclaredOutput(t *testing.T) {
	a := &Executor{}
	cfg := agentConfig{RetryMode: "continue", Sendback: "fix the check"}
	prompt := a.agentPrompt(t.Context(), &workflow.Run{}, &cfg, workflow.Source{}, nil)
	if strings.Contains(strings.ToLower(prompt), "report") || strings.Contains(prompt, "output file") || strings.Contains(prompt, "to describe the WHOLE change") {
		t.Fatalf("step without output was told to write a report: %s", prompt)
	}
	cfg.Output.File = ".apptown/REPORT.md"
	prompt = a.agentPrompt(t.Context(), &workflow.Run{}, &cfg, workflow.Source{}, nil)
	if !strings.Contains(prompt, "update .apptown/REPORT.md to describe the WHOLE change") {
		t.Fatalf("declared report was not named: %s", prompt)
	}
}
