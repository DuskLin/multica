package localgithub

// SyncPageSize bounds each candidate response; cursors are issue UUIDs.
const SyncPageSize = 50

// MaxSyncPullRequests prevents unbounded CLI work from one issue's output.
const MaxSyncPullRequests = 100

// SyncIssue carries only explicit PR references, never task output or credentials.
type SyncIssue struct {
	IssueID    string      `json:"issue_id"`
	Revision   int64       `json:"revision"`
	TaskCount  int64       `json:"task_count"`
	References []Reference `json:"references"`
}

// SyncPage is a bounded, workspace- and machine-scoped candidate batch.
type SyncPage struct {
	Issues     []SyncIssue `json:"issues"`
	NextCursor string      `json:"next_cursor"`
}

// SyncReport submits a complete delivery so partial reads cannot close an issue.
type SyncReport struct {
	Issue        SyncIssue     `json:"issue"`
	PullRequests []PullRequest `json:"pull_requests"`
}
