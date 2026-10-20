package daemon

import (
	"context"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/issuestatus"
)

// gcLocalScriptWorkspaces shares the daemon's configured GC cadence (2h by
// default). Only explicitly done issues qualify, without the ordinary env TTL.
// Missing/closed/cancelled issues and API failures never authorize deletion.
func (d *Daemon) gcLocalScriptWorkspaces(ctx context.Context, stats *gcStats) {
	if d.client == nil {
		return
	}
	sources, err := execenv.ListLocalScriptSources(d.cfg.WorkspacesRoot)
	if err != nil {
		stats.problems = append(stats.problems, WorkdirResult{d.cfg.WorkspacesRoot, err.Error()})
		d.logger.Warn("gc: cannot list script sources", "error", err)
		return
	}
	for _, source := range sources {
		if ctx.Err() != nil {
			return
		}
		issues, err := execenv.ListLocalScriptIssues(source)
		if err != nil {
			stats.problems = append(stats.problems, WorkdirResult{source.LocalPath, err.Error()})
			d.logger.Warn("gc: cannot inspect script source", "source", source.LocalPath, "error", err)
			continue
		}
		for start := 0; start < len(issues); start += issueGCBatchSize {
			end := min(start+issueGCBatchSize, len(issues))
			ids := make([]string, 0, end-start)
			for _, issue := range issues[start:end] {
				if stats.progress != nil {
					stats.progress(workdirGCResult(stats))
				}
				ids = append(ids, issue.IssueID)
			}
			results, err := d.client.GetIssueGCChecks(ctx, source.WorkspaceID, ids)
			if err != nil {
				stats.skipped += len(ids)
				stats.problems = append(stats.problems, WorkdirResult{source.LocalPath, "Task status lookup failed"})
				d.logger.Warn("gc: script issue status lookup failed", "workspace", source.WorkspaceID, "error", err)
				continue
			}
			for _, issue := range issues[start:end] {
				if stats.progress != nil {
					stats.progress(workdirGCResult(stats))
				}
				if ctx.Err() != nil {
					return
				}
				if !scriptIssueDone(results[issue.IssueID]) {
					stats.skipped++
					continue
				}
				path, _ := issue.WorkspacePath()
				execenv.ReportWorkdirProgress(ctx, "measuring", path, 0)
				size := dirSize(path)
				collected, err := execenv.CollectLocalScriptWorkspace(ctx, issue, func(ctx context.Context) (bool, error) {
					latest, err := d.client.GetIssueGCChecks(ctx, issue.WorkspaceID, []string{issue.IssueID})
					if err != nil {
						return false, err
					}
					return scriptIssueDone(latest[issue.IssueID]), nil
				})
				if err != nil {
					stats.skipped++
					stats.problems = append(stats.problems, WorkdirResult{path, err.Error()})
					d.logger.Info("gc: script workspace preserved", "issue", issue.IssueID, "source", source.LocalPath, "reason", err)
					continue
				}
				if collected {
					stats.scriptWorkspacesReclaimed++
					stats.bytesReclaimed += size
					d.logger.Info("gc: script workspace reclaimed; remote branches recorded", "issue", issue.IssueID, "source", source.LocalPath)
				}
			}
		}
	}
}

// scriptIssueDone accepts custom statuses in the done category and the legacy
// done wire value only when no category was supplied by an older server.
func scriptIssueDone(result IssueGCCheckResult) bool {
	if result.Err != nil || !result.Found {
		return false
	}
	if result.Category != "" {
		return result.Category == issuestatus.CategoryDone
	}
	return result.Status == issuestatus.Done
}
