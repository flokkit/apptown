package workflowexec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	piharness "github.com/flokkit/apptown/internal/apptown/agentharness/pi"
	"github.com/flokkit/apptown/internal/execution"
)

// Tool output repeated in Pi's event variants can exceed the worker's result
// limit. The production capture must still deliver the whole answer and report.
// Kills: bypassing the tool-string length guard in the capture filter.
func TestLargeToolEventsKeepTheAnswerAndReport(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input, err := os.Create(filepath.Join(dir, "input.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	log := "BUILD START\n" + strings.Repeat("download 日志\n", 200000) + "BUILD SUCCESS\n"
	result := map[string]any{"role": "toolResult", "content": []any{map[string]any{"type": "text", "text": log}}, "details": map[string]any{"output": log, "usage": map[string]int{"input": 17}}}
	enc := json.NewEncoder(input)
	for i := 0; i < 4; i++ {
		for _, event := range []any{
			map[string]any{"type": "tool_execution_update", "partialResult": result},
			map[string]any{"type": "tool_execution_end", "result": result},
			map[string]any{"type": "message_end", "message": result},
			map[string]any{"type": "turn_end", "toolResults": []any{result}},
			map[string]any{"type": "agent_end", "messages": []any{result}},
		} {
			if err := enc.Encode(event); err != nil {
				t.Fatal(err)
			}
		}
	}
	answer := "Completed.\n" + strings.Repeat("whole answer 日志\n", 2000)
	if err := enc.Encode(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": answer}}}}); err != nil {
		t.Fatal(err)
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(input.Name())
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= maxEventsResult {
		t.Fatalf("fixture only %d bytes", info.Size())
	}
	argv := piharness.CapturedArgv([]string{"sh", "-c", "cat input.jsonl; echo '# completed report' > REPORT.md"}, "events.jsonl", "status")
	out := (execution.Runner{}).Run(t.Context(), execution.Local, execution.Request{Workspace: dir, Argv: argv, Timeout: time.Minute})
	if out.ExitCode != 0 || out.Termination != execution.Exited {
		t.Fatalf("capture failed: %+v", out)
	}
	captured, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	file := execution.ResultFile{State: execution.ResultPresent, Content: captured, Size: int64(len(captured))}
	if len(captured) > maxEventsResult {
		file = execution.ResultFile{State: execution.ResultTooLarge, Size: int64(len(captured))}
	}
	events, err := invocationEvents(map[string]execution.ResultFile{"events": file}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text, _, _, _ := piharness.ParseOutput(events)
	if text != answer {
		t.Fatalf("answer was lost or cut: %d bytes", len(text))
	}
	h := harvestWorkspace(dir, time.Now().Add(-time.Minute), text, "REPORT.md")
	if h.Output != "# completed report\n" || h.MissingOutput != "" || h.Problem != "" {
		t.Fatalf("report: %+v", h)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(events)), "\n")[:16] {
		var event any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(line, "BUILD START") || !strings.Contains(line, "BUILD SUCCESS") || !strings.Contains(line, "bytes of tool output]") || !strings.Contains(line, `"input":17`) || strings.Contains(line, "�") {
			t.Fatalf("tool event lost boundaries, usage or cut notice: %.200s", line)
		}
	}
}

// Cumulative updates must not consume the result-file budget, even when each
// update already fits the string bound. Stdout still carries bounded updates.
// Kills: persisting tool_execution_update events in the capture filter.
func TestFrequentToolUpdatesKeepTheAnswerAndEndResult(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input, err := os.Create(filepath.Join(dir, "input.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(input)
	partial := "BUILD START" + strings.Repeat("x", 16*1024-len("BUILD STARTBUILD SUCCESS")) + "BUILD SUCCESS"
	for i := 0; i < 5000; i++ {
		if err := enc.Encode(map[string]any{"type": "tool_execution_update", "partialResult": map[string]any{"output": partial}}); err != nil {
			t.Fatal(err)
		}
	}
	// Also prove that stdout still bounds updates exceeding 16 KiB.
	end := partial + partial
	for _, event := range []any{
		map[string]any{"type": "tool_execution_update", "partialResult": map[string]any{"output": end}},
		map[string]any{"type": "tool_execution_end", "result": map[string]any{"output": end}},
		map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "Completed build."}}}},
	} {
		if err := enc.Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	updates, boundedUpdates := 0, 0
	argv := piharness.CapturedArgv([]string{"cat", "input.jsonl"}, "events.jsonl", "status")
	out := (execution.Runner{}).Run(t.Context(), execution.Local, execution.Request{
		Workspace: dir, Argv: argv, Timeout: time.Minute,
		OnLine: func(line string) {
			var event struct {
				Type    string `json:"type"`
				Partial struct {
					Output string `json:"output"`
				} `json:"partialResult"`
			}
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				t.Error(err)
			}
			if event.Type == "tool_execution_update" {
				updates++
				if len(event.Partial.Output) > 17*1024 {
					t.Errorf("live update not bounded: %d bytes", len(event.Partial.Output))
				}
				if strings.Contains(event.Partial.Output, "bytes of tool output]") {
					boundedUpdates++
				}
			}
		},
	})
	if out.ExitCode != 0 || out.Termination != execution.Exited {
		t.Fatalf("capture failed: %+v", out)
	}
	if updates != 5001 || boundedUpdates != 1 {
		t.Fatalf("live updates: %d, bounded: %d", updates, boundedUpdates)
	}
	captured, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(captured) > 32*1024 {
		t.Fatalf("events file grew with cumulative updates: %d bytes (limit %d)", len(captured), maxEventsResult)
	}
	lines := strings.Split(strings.TrimSpace(string(captured)), "\n")
	if len(lines) != 2 || strings.Contains(string(captured), "tool_execution_update") {
		t.Fatalf("events file must contain only the end result and answer: %d lines", len(lines))
	}
	var event struct {
		Type   string `json:"type"`
		Result struct {
			Output string `json:"output"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "tool_execution_end" || !strings.HasPrefix(event.Result.Output, "BUILD START") || !strings.HasSuffix(event.Result.Output, "BUILD SUCCESS") || !strings.Contains(event.Result.Output, "bytes of tool output]") || len(event.Result.Output) > 17*1024 {
		t.Fatalf("end result lost boundaries or bound: %.100s", lines[0])
	}
	if text, _, _, _ := piharness.ParseOutput(captured); text != "Completed build." {
		t.Fatalf("answer lost: %q", text)
	}
}
