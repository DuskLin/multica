package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// streamScriptPreparation tails the helper's existing log, keeping the helper
// protocol and process-tree cancellation unchanged. Stop joins the reporter
// before agent messages can consume the shared sequence counter.
func (d *Daemon) streamScriptPreparation(taskID, root string, seq *atomic.Int32) func(error) {
	done := make(chan error, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		callID := uuid.NewString()
		report := func(msg TaskMessageData) {
			msg.Seq = int(seq.Add(1))
			msg.CreatedAt = time.Now().UTC()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := d.client.ReportTaskMessages(ctx, taskID, []TaskMessageData{msg}); err != nil {
				d.logger.Debug("report workspace initialization", "task", taskID, "error", err)
			}
		}
		report(TaskMessageData{Type: "tool_use", Tool: "Workspace initialization", CallID: callID})
		var offset int64
		const limit = 256 * 1024
		var sent int
		truncated := false
		tail := func(final bool) {
			file, err := os.Open(filepath.Join(root, "logs", "init-workspace.log"))
			if err != nil {
				return
			}
			defer file.Close()
			size := min(32*1024, limit-sent)
			if final {
				size = limit - sent
			}
			data := make([]byte, size+1)
			n, _ := file.ReadAt(data, offset)
			if n == 0 {
				return
			}
			more := n > size
			data = data[:min(n, size)]
			// Preserve incomplete UTF-8 at a chunk boundary; replace invalid
			// bytes inside the log so binary output cannot corrupt JSON.
			if !final && len(data) > 0 {
				last := len(data) - 1
				for last > 0 && !utf8.RuneStart(data[last]) {
					last--
				}
				if !utf8.FullRune(data[last:]) {
					data = data[:last]
				}
			}
			offset += int64(len(data))
			sent += len(data)
			if len(data) > 0 {
				report(TaskMessageData{Type: "tool_result", Tool: "Initialization output", Output: strings.ToValidUTF8(string(data), "�")})
			}
			if sent >= limit && more && !truncated {
				truncated = true
				report(TaskMessageData{Type: "tool_result", Tool: "Initialization output", Output: "\nLive output limit reached; full output remains in init-workspace.log.\n"})
			}
		}

		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				tail(false)
			case err := <-done:
				tail(true)
				output := "Workspace ready"
				if err != nil {
					output = fmt.Sprintf("Workspace initialization failed: %v", err)
				}
				report(TaskMessageData{Type: "tool_result", Tool: "Workspace initialization", CallID: callID, Output: output})
				return
			}
		}
	}()
	return func(err error) { done <- err; <-joined }
}
