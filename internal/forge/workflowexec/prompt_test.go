package workflowexec

import (
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/workflow"
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
