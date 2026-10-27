//go:build agentintegration

package agent

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	dshSmokeTurnTimeout = 90 * time.Second
	dshSmokeTurnCount   = 3
)

// TestDshRealRuntimeSmoke is opt-in because it calls the configured DeepSeek
// model and consumes API quota. It exercises the complete Multica backend,
// installed DSH profile, model provider, session resume, and shell-tool path.
func TestDshRealRuntimeSmoke(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" {
		t.Skip("set MULTICA_RUN_REAL_AGENT_SMOKE=1 to run the DSH integration smoke")
	}
	path := os.Getenv("MULTICA_DSH_PATH")
	if path == "" {
		t.Fatal("MULTICA_DSH_PATH is required")
	}
	b, err := New("dsh", Config{ExecutablePath: path, TaskID: "dsh-real-smoke", Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), dshSmokeTurnCount*dshSmokeTurnTimeout)
	defer cancel()
	cwd := t.TempDir()
	model := os.Getenv("MULTICA_DSH_MODEL")
	session, err := b.Execute(ctx, "Remember the code word PINEAPPLE. Reply with exactly DSH_MULTICA_OK and nothing else.", ExecOptions{
		Cwd: cwd, Model: model,
		Timeout: dshSmokeTurnTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range session.Messages {
	}
	result := <-session.Result
	if result.Status != "completed" {
		t.Fatalf("DSH smoke failed: status=%q error=%q", result.Status, result.Error)
	}
	if strings.TrimSpace(result.Output) != "DSH_MULTICA_OK" {
		t.Fatalf("unexpected DSH output: %q", result.Output)
	}
	if result.SessionID == "" {
		t.Fatal("DSH smoke returned no session ID")
	}

	resumed, err := b.Execute(ctx, "Reply with exactly the code word from the previous turn and nothing else.", ExecOptions{
		Cwd: cwd, Model: model,
		ResumeSessionID: result.SessionID, Timeout: dshSmokeTurnTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range resumed.Messages {
	}
	resumeResult := <-resumed.Result
	if resumeResult.Status != "completed" {
		t.Fatalf("DSH resume smoke failed: status=%q error=%q", resumeResult.Status, resumeResult.Error)
	}
	if strings.TrimSpace(resumeResult.Output) != "PINEAPPLE" {
		t.Fatalf("DSH resume lost conversation context: %q", resumeResult.Output)
	}
	if resumeResult.SessionID != result.SessionID {
		t.Fatalf("DSH resume changed session ID: %q -> %q", result.SessionID, resumeResult.SessionID)
	}

	const toolMarker = "DSH_TOOL_SMOKE_OK"
	toolSession, err := b.Execute(ctx, "Run the bash tool once with the command printf "+toolMarker+". Then reply exactly "+toolMarker+". Do not access files, networks, or any external service through tools.", ExecOptions{
		Cwd: cwd, Model: model,
		ResumeSessionID: result.SessionID, Timeout: dshSmokeTurnTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	sawToolResult := false
	for message := range toolSession.Messages {
		if message.Type == MessageToolResult && message.Tool == "bash" && strings.Contains(message.Output, toolMarker) {
			sawToolResult = true
		}
	}
	toolResult := <-toolSession.Result
	if toolResult.Status != "completed" {
		t.Fatalf("DSH tool smoke failed: status=%q error=%q", toolResult.Status, toolResult.Error)
	}
	if !sawToolResult || strings.TrimSpace(toolResult.Output) != toolMarker {
		t.Fatalf("DSH tool execution was not confirmed: saw_result=%v output=%q", sawToolResult, toolResult.Output)
	}
}
