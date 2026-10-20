package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// awaitWorkdirJob waits only on isolated test jobs, never on user directories.
func awaitWorkdirJob(t *testing.T, d *Daemon, id string) WorkdirJob {
	t.Helper()
	deadline := time.After(20 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		for _, job := range d.workdirJobSnapshot().Operations {
			if job.ID == id && !job.finishedAt.IsZero() {
				return job
			}
		}
		select {
		case <-deadline:
			t.Fatal("background cleanup did not finish")
		case <-ticker.C:
		}
	}
}

func TestWorkdirJobsAcknowledgeQueueDeduplicateAndKeepProtection(t *testing.T) {
	d := newGCTestDaemon(t, http.NotFoundHandler())
	ctx, cancel := context.WithCancel(context.Background())
	d.setLifecycleCtx(ctx)
	defer cancel()
	_, path := scriptGCDaemonFixture(t, d)
	if err := os.WriteFile(filepath.Join(path, "unpublished.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	d.workdirOperations.Lock()
	locked := true
	defer func() {
		if locked {
			d.workdirOperations.Unlock()
		}
	}()
	response := workdirTestRequest(t, d, "start-delete", workdirID(path), "test-token", "")
	if response.Code != http.StatusOK {
		t.Fatalf("submission failed: %s", response.Body)
	}
	var accepted WorkdirJobResponse
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil || len(accepted.Operations) != 1 {
		t.Fatalf("invalid acknowledgment: %v %s", err, response.Body)
	}
	job := accepted.Operations[0]
	duplicate, err := d.enqueueWorkdirJob("delete", workdirID(path))
	if err != nil || duplicate.Operations[0].ID != job.ID || job.Status != "queued" {
		t.Fatalf("active target was not deduplicated: %+v %v", duplicate, err)
	}
	status := workdirTestRequest(t, d, "operations", "", "test-token", "")
	if status.Code != http.StatusOK {
		t.Fatalf("polling waited on cleanup lock: %s", status.Body)
	}
	d.workdirOperations.Unlock()
	locked = false
	completed := awaitWorkdirJob(t, d, job.ID)
	if completed.Status != "failed" || completed.Error == "" || completed.Result.Removed != 0 {
		t.Fatalf("unpublished work was not rejected: %+v", completed)
	}
	if _, err := os.Stat(filepath.Join(path, "unpublished.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestWorkdirJobsDeleteAfterRequestReturnsAndRetainResult(t *testing.T) {
	d := newGCTestDaemon(t, http.NotFoundHandler())
	_, path := scriptGCDaemonFixture(t, d)
	accepted, err := d.enqueueWorkdirJob("delete", workdirID(path))
	if err != nil {
		t.Fatal(err)
	}
	job := awaitWorkdirJob(t, d, accepted.Operations[0].ID)
	if job.Status != "succeeded" || job.Result.Removed != 1 || job.Path != path || job.ElapsedMS <= 0 {
		t.Fatalf("unexpected completion: %+v", job)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("target still exists: %v", err)
	}
	if len(d.workdirJobSnapshot().Operations) != 1 {
		t.Fatal("returning to settings lost the completed operation")
	}
}

func TestWorkdirJobsCancelQueuedWorkOnDaemonShutdown(t *testing.T) {
	d := newGCTestDaemon(t, http.NotFoundHandler())
	ctx, cancel := context.WithCancel(context.Background())
	d.setLifecycleCtx(ctx)
	cancel()
	accepted, err := d.enqueueWorkdirJob("delete", workdirID(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	job := awaitWorkdirJob(t, d, accepted.Operations[0].ID)
	if job.Status != "failed" || job.Error != context.Canceled.Error() {
		t.Fatalf("shutdown did not stop queued work: %+v", job)
	}
}

func TestWorkdirJobsReclaimUsesDonePolicyAndReportsResults(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/daemon/workspaces/script-workspace/issues/gc-check", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string][]IssueGCCheckResult{"issues": {{ID: "script-issue", Found: true, Status: "done", Category: "done"}}})
	})
	d := newGCTestDaemon(t, mux)
	_, path := scriptGCDaemonFixture(t, d)
	response := workdirTestRequest(t, d, "start-reclaim", "", "test-token", "")
	var accepted WorkdirJobResponse
	if response.Code != http.StatusOK {
		t.Fatalf("reclaim submission: %d %s", response.Code, response.Body)
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil || len(accepted.Operations) != 1 {
		t.Fatalf("invalid acknowledgment: %v %s", err, response.Body)
	}
	job := awaitWorkdirJob(t, d, accepted.Operations[0].ID)
	if job.Status != "succeeded" || job.Result.Removed != 1 || job.Result.ReclaimedBytes <= 0 {
		t.Fatalf("wrong background result: %+v", job)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("completed directory still exists: %v", err)
	}
}
