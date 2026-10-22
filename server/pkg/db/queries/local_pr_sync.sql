-- name: ListLocalPRSyncIssues :many
-- Local-directory delivery uses explicit URLs, including those from nested repos.
-- Include already linked PRs so later merges refresh even if delivery text changes.
SELECT i.id, i.revision,
    (SELECT count(*) FROM agent_task_queue task WHERE task.issue_id = i.id)::bigint AS task_count,
    (COALESCE(i.description, '') || E'\n' ||
     COALESCE((SELECT string_agg(t.result->>'output', E'\n' ORDER BY t.created_at)
               FROM agent_task_queue t
               WHERE t.issue_id = i.id
                 AND t.status = 'completed'), '') || E'\n' ||
     COALESCE((SELECT string_agg(pr.html_url, E'\n' ORDER BY pr.html_url)
               FROM issue_pull_request link
               JOIN github_pull_request pr ON pr.id = link.pull_request_id
               WHERE link.issue_id = i.id AND pr.workspace_id = i.workspace_id), ''))::text AS delivery,
    EXISTS(SELECT 1 FROM agent_task_queue t
           WHERE t.issue_id = i.id
             AND t.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred'))::boolean AS unfinished,
    EXISTS(SELECT 1 FROM issue_pull_request link
           JOIN github_pull_request pr ON pr.id = link.pull_request_id
           WHERE link.issue_id = i.id AND pr.workspace_id = i.workspace_id
             AND pr.state <> 'merged')::boolean AS had_unmerged_pr
FROM issue i
WHERE i.workspace_id = sqlc.arg(workspace_id)
  AND (sqlc.narg(issue_id)::uuid IS NULL OR i.id = sqlc.narg(issue_id))
  AND (sqlc.narg(after_id)::uuid IS NULL OR i.id > sqlc.narg(after_id))
  AND EXISTS(SELECT 1 FROM project_resource resource
             WHERE resource.project_id = i.project_id
               AND resource.workspace_id = i.workspace_id
               AND resource.resource_type = 'local_directory'
               AND (sqlc.narg(daemon_id)::text IS NULL OR resource.resource_ref->>'daemon_id' = sqlc.narg(daemon_id)))
  AND (sqlc.narg(daemon_id)::text IS NULL OR EXISTS(
      SELECT 1 FROM agent_task_queue task
      JOIN agent_runtime runtime ON runtime.id = task.runtime_id
      WHERE task.issue_id = i.id AND task.status = 'completed'
        AND runtime.workspace_id = i.workspace_id
        AND runtime.daemon_id = sqlc.narg(daemon_id)))
ORDER BY i.id
LIMIT NULLIF(sqlc.arg(max_issues)::int, 0);

-- name: LockLocalPRSyncIssue :one
-- Reject a delivery snapshot if its issue changed while GitHub was being read.
SELECT * FROM issue WHERE id = $1 AND workspace_id = $2 FOR UPDATE;

-- name: GetLocalPRSyncTaskState :one
-- Recheck delivery readiness after locking the issue, before committing links.
SELECT count(*)::bigint AS task_count,
    COALESCE(bool_or(task.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')), false)::boolean AS unfinished
FROM agent_task_queue task
JOIN issue ON issue.id = task.issue_id
WHERE task.issue_id = $1 AND issue.workspace_id = $2;
