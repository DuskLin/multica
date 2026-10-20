package daemon

import (
	"context"
	"crypto/rand"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

const workdirJobLimit = 128
const workdirLockPoll = 100 * time.Millisecond

// WorkdirJob is a daemon-owned background operation. Polling never scans disk.
type WorkdirJob struct {
	ID             string              `json:"id"`
	Profile        string              `json:"profile"`
	DirectoryID    string              `json:"directory_id"`
	Action         string              `json:"action"`
	Status         string              `json:"status"`
	Stage          string              `json:"stage"`
	Path           string              `json:"path"`
	CurrentPath    string              `json:"current_path"`
	ElapsedMS      int64               `json:"elapsed_ms"`
	StageElapsedMS int64               `json:"stage_elapsed_ms"`
	RemovedFiles   int64               `json:"removed_files"`
	Result         WorkdirActionResult `json:"result"`
	Error          string              `json:"error"`
	createdAt      time.Time
	stageAt        time.Time
	finishedAt     time.Time
}

// workdirJobQueue serializes user operations independently of request lifetimes.
type workdirJobQueue struct {
	mu      sync.Mutex
	jobs    []WorkdirJob
	running bool
}

// WorkdirJobResponse is also used for one acknowledged queued operation.
type WorkdirJobResponse struct {
	Operations []WorkdirJob `json:"operations"`
}

// snapshotWorkdirJob copies mutable results and computes elapsed wall time.
func snapshotWorkdirJob(job WorkdirJob) WorkdirJob {
	now := job.finishedAt
	if now.IsZero() {
		now = time.Now()
	}
	job.ElapsedMS = now.Sub(job.createdAt).Milliseconds()
	job.StageElapsedMS = now.Sub(job.stageAt).Milliseconds()
	job.Result.Problems = slices.Clone(job.Result.Problems)
	return job
}

// workdirJobSnapshot retains completed results until bounded history is evicted.
func (d *Daemon) workdirJobSnapshot() WorkdirJobResponse {
	d.workdirJobs.mu.Lock()
	defer d.workdirJobs.mu.Unlock()
	response := WorkdirJobResponse{Operations: []WorkdirJob{}}
	for _, job := range d.workdirJobs.jobs {
		response.Operations = append(response.Operations, snapshotWorkdirJob(job))
	}
	return response
}

// enqueueWorkdirJob acknowledges after acceptance, deduplicating active targets.
// No path is accepted from the caller; ownership is resolved again by the worker.
func (d *Daemon) enqueueWorkdirJob(action, directoryID string) (WorkdirJobResponse, error) {
	d.workdirJobs.mu.Lock()
	defer d.workdirJobs.mu.Unlock()
	for _, job := range d.workdirJobs.jobs {
		if job.Action == action && job.DirectoryID == directoryID && job.finishedAt.IsZero() {
			return WorkdirJobResponse{Operations: []WorkdirJob{snapshotWorkdirJob(job)}}, nil
		}
	}
	for len(d.workdirJobs.jobs) >= workdirJobLimit {
		index := slices.IndexFunc(d.workdirJobs.jobs, func(job WorkdirJob) bool { return !job.finishedAt.IsZero() })
		if index < 0 {
			return WorkdirJobResponse{}, errors.New("Cleanup queue is full")
		}
		d.workdirJobs.jobs = slices.Delete(d.workdirJobs.jobs, index, index+1)
	}
	now := time.Now()
	job := WorkdirJob{
		ID: rand.Text(), Profile: d.cfg.Profile, DirectoryID: directoryID,
		Action: action, Status: "queued", Stage: "queued", createdAt: now, stageAt: now,
		Result: WorkdirActionResult{Problems: []WorkdirResult{}},
	}
	d.workdirJobs.jobs = append(d.workdirJobs.jobs, job)
	if !d.workdirJobs.running {
		d.workdirJobs.running = true
		go d.runWorkdirJobs()
	}
	return WorkdirJobResponse{Operations: []WorkdirJob{snapshotWorkdirJob(job)}}, nil
}

// updateWorkdirJob serializes worker updates with status readers.
func (d *Daemon) updateWorkdirJob(id string, update func(*WorkdirJob)) {
	d.workdirJobs.mu.Lock()
	defer d.workdirJobs.mu.Unlock()
	for i := range d.workdirJobs.jobs {
		if d.workdirJobs.jobs[i].ID == id {
			update(&d.workdirJobs.jobs[i])
			return
		}
	}
}

// runWorkdirJobs runs accepted operations FIFO without retaining HTTP contexts.
func (d *Daemon) runWorkdirJobs() {
	for {
		d.workdirJobs.mu.Lock()
		index := slices.IndexFunc(d.workdirJobs.jobs, func(job WorkdirJob) bool { return job.Status == "queued" })
		if index < 0 {
			d.workdirJobs.running = false
			d.workdirJobs.mu.Unlock()
			return
		}
		job := d.workdirJobs.jobs[index]
		d.workdirJobs.mu.Unlock()
		d.runWorkdirJob(job)
	}
}

// runWorkdirJob shares the cleanup exclusion lock and all existing protections.
func (d *Daemon) runWorkdirJob(job WorkdirJob) {
	ctx, cancel := context.WithTimeout(d.daemonLifecycleCtx(), WorkdirOperationTimeout)
	defer cancel()
	ctx = execenv.WithWorkdirProgress(ctx, func(progress execenv.WorkdirProgress) {
		d.updateWorkdirJob(job.ID, func(current *WorkdirJob) {
			if progress.Stage != "" && progress.Stage != current.Stage {
				current.Stage = progress.Stage
				current.stageAt = time.Now()
			}
			if progress.Path != "" {
				current.CurrentPath = progress.Path
			}
			current.RemovedFiles += progress.RemovedFiles
		})
	})
	result := WorkdirActionResult{Problems: []WorkdirResult{}}
	err := d.lockWorkdirJob(ctx)
	if err == nil {
		d.updateWorkdirJob(job.ID, func(current *WorkdirJob) { current.Status = "running" })
		execenv.ReportWorkdirProgress(ctx, "locating", "", 0)
		if job.Action == "delete" {
			var row WorkDirectory
			row, err = d.findWorkDirectory(ctx, job.DirectoryID)
			if err == nil {
				d.updateWorkdirJob(job.ID, func(current *WorkdirJob) { current.Path = row.Path })
				result.ReclaimedBytes, err = d.deleteWorkDirectoryRow(ctx, row)
				if err == nil {
					result.Removed = 1
				}
			}
		} else {
			stats := &gcStats{byPattern: map[string]int{}, progressCtx: ctx, progress: func(result WorkdirActionResult) {
				d.updateWorkdirJob(job.ID, func(current *WorkdirJob) { current.Result = result })
			}}
			d.runGCCycle(ctx, stats)
			result = workdirGCResult(stats)
			err = ctx.Err()
		}
		d.workdirOperations.Unlock()
	}
	d.updateWorkdirJob(job.ID, func(current *WorkdirJob) {
		current.Result = result
		current.finishedAt = time.Now()
		current.Status = "succeeded"
		if err != nil {
			current.Status = "failed"
			current.Error = err.Error()
		}
	})
}

// lockWorkdirJob waits for scheduled cleanup without making status reads wait.
func (d *Daemon) lockWorkdirJob(ctx context.Context) error {
	ticker := time.NewTicker(workdirLockPoll)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.workdirOperations.TryLock() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// workdirGCResult projects the canonical GC counters into the API result.
func workdirGCResult(stats *gcStats) WorkdirActionResult {
	return WorkdirActionResult{
		Removed: stats.cleaned + stats.orphaned + stats.scriptWorkspacesReclaimed,
		Skipped: stats.skipped, ReclaimedBytes: stats.bytesReclaimed,
		Problems: append([]WorkdirResult{}, stats.problems...),
	}
}
