package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/integrations/localgithub"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type prSyncFixture struct {
	runtimeID, daemonID, projectID, resourceID, issueID, taskID string
}

// newPRSyncFixture creates an isolated delivery with two nested repository PRs.
func newPRSyncFixture(t *testing.T) prSyncFixture {
	t.Helper()
	f := prSyncFixture{daemonID: uuid.NewString()}
	f.runtimeID = dbfx.Runtime(t, "PR sync runtime", testutil.Cols{"daemon_id": f.daemonID, "owner_id": testUserID})
	f.projectID = dbfx.Insert(t, "project", testutil.Cols{"workspace_id": testWorkspaceID, "title": "PR sync project"})
	ref, err := json.Marshal(map[string]string{"daemon_id": f.daemonID, "path": "/fake/project"})
	if err != nil {
		t.Fatal(err)
	}
	f.resourceID = dbfx.Insert(t, "project_resource", testutil.Cols{
		"workspace_id": testWorkspaceID, "project_id": f.projectID, "resource_type": "local_directory", "resource_ref": ref,
	})
	f.issueID = dbfx.Issue(t, "Daemon delivery", testutil.Cols{"project_id": f.projectID, "status": "in_review"})
	agentID := dbfx.Agent(t, "PR delivery agent "+f.daemonID, f.runtimeID)
	output := "https://github.com/test-" + f.issueID + "/service/pull/42\nhttps://github.com/test-" + f.issueID + "/web/pull/43"
	result, err := json.Marshal(map[string]string{"output": output})
	if err != nil {
		t.Fatal(err)
	}
	f.taskID = dbfx.Task(t, agentID, testutil.Cols{"runtime_id": f.runtimeID, "issue_id": f.issueID, "status": "completed", "result": result})
	dbfx.Cleanup(t, "DELETE FROM github_pull_request WHERE workspace_id = $1 AND repo_owner = $2", testWorkspaceID, "test-"+f.issueID)
	dbfx.Cleanup(t, "DELETE FROM issue_pull_request_exclusion WHERE issue_id = $1", f.issueID)
	dbfx.Cleanup(t, "DELETE FROM issue_pull_request WHERE issue_id = $1", f.issueID)
	return f
}

// request uses normal owner authentication; tests can replace it with daemon-token scope.
func (f prSyncFixture) request(method string, body *localgithub.SyncReport) *http.Request {
	return withURLParam(newRequest(method, "/api/daemon/runtimes/"+f.runtimeID+"/pull-requests/sync", body), "runtimeId", f.runtimeID)
}

// report creates valid fake metadata, without resolving any installed GitHub CLI.
func (f prSyncFixture) report(t *testing.T) localgithub.SyncReport {
	t.Helper()
	var page localgithub.SyncPage
	testutil.Call(t, testHandler.ListDaemonPRSyncIssues, f.request(http.MethodGet, nil)).Want(http.StatusOK).JSON(&page)
	if len(page.Issues) != 1 || page.Issues[0].IssueID != f.issueID {
		t.Fatalf("unexpected candidates: %+v", page)
	}
	report := localgithub.SyncReport{Issue: page.Issues[0]}
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, ref := range report.Issue.References {
		report.PullRequests = append(report.PullRequests, localgithub.PullRequest{
			Number: ref.Number, URL: ref.URL, Title: "Nested fix", State: "open", CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
		})
	}
	return report
}

func TestDaemonPRSyncDelivery(t *testing.T) {
	setWorkspacePRMergeStatusForTest(t, "done")
	f := newPRSyncFixture(t)
	report := f.report(t)
	if len(report.PullRequests) != 2 {
		t.Fatalf("nested PR count = %d", len(report.PullRequests))
	}
	submit := func(report localgithub.SyncReport) {
		t.Helper()
		testutil.Call(t, testHandler.ReportDaemonPRSync, f.request(http.MethodPost, &report)).Want(http.StatusNoContent)
	}
	submit(report)
	submit(report)
	if linkedPRCountForTest(t, f.issueID) != 2 || issueStatusForTest(t, f.issueID) != "in_review" {
		t.Fatal("open delivery not linked idempotently")
	}
	// One merge must not complete a multi-repository issue.
	now := time.Now().UTC()
	report.PullRequests[0].State, report.PullRequests[0].MergedAt, report.PullRequests[0].UpdatedAt = "closed", &now, now
	submit(report)
	if issueStatusForTest(t, f.issueID) != "in_review" {
		t.Fatal("partial merge completed issue")
	}
	report.PullRequests[1].State, report.PullRequests[1].MergedAt, report.PullRequests[1].UpdatedAt = "closed", &now, now
	submit(report)
	if issueStatusForTest(t, f.issueID) != "done" {
		t.Fatal("all merged delivery did not complete issue")
	}
	// Refetch after manual reopening; an unchanged mirror is not a new merge event.
	dbfx.Exec(t, "UPDATE issue SET status = 'in_review', revision = revision + 1 WHERE id = $1", f.issueID)
	refreshed := f.report(t)
	refreshed.PullRequests = report.PullRequests
	submit(refreshed)
	if issueStatusForTest(t, f.issueID) != "in_review" {
		t.Fatal("unchanged merged PRs reclosed a reopened issue")
	}
	pr, err := testHandler.Queries.GetGitHubPullRequest(context.Background(), db.GetGitHubPullRequestParams{
		WorkspaceID: parseUUID(testWorkspaceID), RepoOwner: report.Issue.References[0].Owner, RepoName: "service", PrNumber: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Exec(t, "DELETE FROM issue_pull_request WHERE issue_id = $1 AND pull_request_id = $2", f.issueID, pr.ID)
	if err := testHandler.Queries.ExcludePullRequestFromIssue(context.Background(), db.ExcludePullRequestFromIssueParams{
		WorkspaceID: parseUUID(testWorkspaceID), IssueID: parseUUID(f.issueID), PullRequestID: pr.ID,
	}); err != nil {
		t.Fatal(err)
	}
	submit(refreshed)
	if linkedPRCountForTest(t, f.issueID) != 1 {
		t.Fatal("manual exclusion was undone")
	}
}

func TestDaemonPRSyncRejectsInvalidReports(t *testing.T) {
	for _, scenario := range []string{"partial", "forged PR", "revision", "task count", "new run", "resource moved", "wrong workspace", "wrong daemon", "wrong owner", "disabled", "bad UUID", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPRSyncFixture(t)
			report := f.report(t)
			want := http.StatusBadRequest
			switch scenario {
			case "partial":
				report.PullRequests = report.PullRequests[:1]
			case "forged PR":
				report.PullRequests[0].URL = "https://github.com/other/private/pull/1"
			case "revision":
				report.Issue.Revision++
				want = http.StatusConflict
			case "task count":
				report.Issue.TaskCount++
				want = http.StatusConflict
			case "new run":
				dbfx.Exec(t, "UPDATE agent_task_queue SET status = 'running' WHERE id = $1", f.taskID)
				want = http.StatusNotFound
			case "resource moved":
				dbfx.Exec(t, "UPDATE project_resource SET resource_ref = jsonb_set(resource_ref, '{daemon_id}', to_jsonb('other-machine'::text)) WHERE id = $1", f.resourceID)
				want = http.StatusNotFound
			case "wrong workspace", "wrong daemon", "wrong owner":
				want = http.StatusNotFound
			case "disabled":
				dbfx.Exec(t, `UPDATE workspace SET settings = '{"github_enabled":false}' WHERE id = $1`, testWorkspaceID)
				t.Cleanup(func() { dbfx.Exec(t, `UPDATE workspace SET settings = '{}' WHERE id = $1`, testWorkspaceID) })
				want = http.StatusForbidden
			case "bad UUID":
				report.Issue.IssueID = "invalid"
			}
			req := f.request(http.MethodPost, &report)
			if scenario == "wrong workspace" || scenario == "wrong daemon" {
				wsID, daemonID := testWorkspaceID, f.daemonID
				if scenario == "wrong workspace" {
					wsID = uuid.NewString()
				} else {
					daemonID = uuid.NewString()
				}
				req = withURLParam(newDaemonTokenRequest(http.MethodPost, req.URL.Path, report, wsID, daemonID), "runtimeId", f.runtimeID)
			}
			if scenario == "wrong owner" {
				req.Header.Set("X-User-ID", createRuntimeLocalSkillTestMember(t, "member"))
			}
			if scenario == "malformed" {
				req = withURLParam(newRequest(http.MethodPost, req.URL.Path, json.RawMessage(`{"issue":`)), "runtimeId", f.runtimeID)
			}
			testutil.Call(t, testHandler.ReportDaemonPRSync, req).Want(want)
			if linkedPRCountForTest(t, f.issueID) != 0 {
				t.Fatal("rejected report wrote PR links")
			}
		})
	}
}

func TestDaemonPRSyncCandidatesAreScoped(t *testing.T) {
	f, other := newPRSyncFixture(t), newPRSyncFixture(t)
	report := f.report(t)
	if report.Issue.IssueID == other.issueID {
		t.Fatal("other machine's delivery leaked")
	}
	agentID := dbfx.Agent(t, "Unfinished agent", f.runtimeID)
	dbfx.Task(t, agentID, testutil.Cols{"runtime_id": f.runtimeID, "issue_id": f.issueID, "status": "queued"})
	var page localgithub.SyncPage
	testutil.Call(t, testHandler.ListDaemonPRSyncIssues, f.request(http.MethodGet, nil)).Want(http.StatusOK).JSON(&page)
	if len(page.Issues) != 0 {
		t.Fatal("unfinished delivery was offered")
	}
}

func TestDaemonPRSyncSharedMergeReachesEveryReadyIssue(t *testing.T) {
	setWorkspacePRMergeStatusForTest(t, "done")
	f := newPRSyncFixture(t)
	report := f.report(t)
	otherIssue := dbfx.Issue(t, "Shared delivery", testutil.Cols{"project_id": f.projectID, "status": "in_review"})
	agentID := dbfx.Agent(t, "Shared delivery agent", f.runtimeID)
	output, err := json.Marshal(map[string]string{"output": report.Issue.References[0].URL + "\n" + report.Issue.References[1].URL})
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Task(t, agentID, testutil.Cols{"runtime_id": f.runtimeID, "issue_id": otherIssue, "status": "completed", "result": output})
	dbfx.Cleanup(t, "DELETE FROM issue_pull_request WHERE issue_id = $1", otherIssue)
	testutil.Call(t, testHandler.ReportDaemonPRSync, f.request(http.MethodPost, &report)).Want(http.StatusNoContent)
	var page localgithub.SyncPage
	testutil.Call(t, testHandler.ListDaemonPRSyncIssues, f.request(http.MethodGet, nil)).Want(http.StatusOK).JSON(&page)
	for _, candidate := range page.Issues {
		if candidate.IssueID != otherIssue {
			continue
		}
		shared := localgithub.SyncReport{Issue: candidate, PullRequests: report.PullRequests}
		testutil.Call(t, testHandler.ReportDaemonPRSync, f.request(http.MethodPost, &shared)).Want(http.StatusNoContent)
	}
	if linkedPRCountForTest(t, otherIssue) != 2 {
		t.Fatal("shared delivery not linked")
	}
	now := time.Now().UTC()
	for index := range report.PullRequests {
		report.PullRequests[index].State = "closed"
		report.PullRequests[index].MergedAt = &now
		report.PullRequests[index].UpdatedAt = now
	}
	testutil.Call(t, testHandler.ReportDaemonPRSync, f.request(http.MethodPost, &report)).Want(http.StatusNoContent)
	if issueStatusForTest(t, otherIssue) != "done" {
		t.Fatal("shared merge only completed the first reported issue")
	}
}

func TestDaemonPRSyncPreservesNewerWebhookSnapshot(t *testing.T) {
	f := newPRSyncFixture(t)
	report := f.report(t)
	testutil.Call(t, testHandler.ReportDaemonPRSync, f.request(http.MethodPost, &report)).Want(http.StatusNoContent)
	dbfx.Exec(t, `UPDATE github_pull_request SET title = 'Newer webhook', pr_updated_at = now() + interval '1 minute'
		WHERE workspace_id = $1 AND repo_owner = $2`, testWorkspaceID, report.Issue.References[0].Owner)
	testutil.Call(t, testHandler.ReportDaemonPRSync, f.request(http.MethodPost, &report)).Want(http.StatusNoContent)
	pr, err := testHandler.Queries.GetGitHubPullRequest(context.Background(), db.GetGitHubPullRequestParams{
		WorkspaceID: parseUUID(testWorkspaceID), RepoOwner: report.Issue.References[0].Owner, RepoName: "service", PrNumber: 42,
	})
	if err != nil || pr.Title != "Newer webhook" {
		t.Fatalf("stale daemon overwrote webhook: %s %v", pr.Title, err)
	}
}

func TestDaemonPRSyncPagination(t *testing.T) {
	f := newPRSyncFixture(t)
	agentID := dbfx.Agent(t, "Paginated delivery agent", f.runtimeID)
	for index := 0; index < localgithub.SyncPageSize; index++ {
		issueID := dbfx.Issue(t, fmt.Sprintf("Page delivery %d", index), testutil.Cols{"project_id": f.projectID})
		dbfx.Task(t, agentID, testutil.Cols{"runtime_id": f.runtimeID, "issue_id": issueID, "status": "completed", "result": json.RawMessage(`{"output":"https://github.com/team/repo/pull/1"}`)})
	}
	var first, second localgithub.SyncPage
	testutil.Call(t, testHandler.ListDaemonPRSyncIssues, f.request(http.MethodGet, nil)).Want(http.StatusOK).JSON(&first)
	if len(first.Issues) != localgithub.SyncPageSize || first.NextCursor == "" {
		t.Fatalf("first page: %+v", first)
	}
	req := f.request(http.MethodGet, nil)
	query := req.URL.Query()
	query.Set("after", first.NextCursor)
	req.URL.RawQuery = query.Encode()
	testutil.Call(t, testHandler.ListDaemonPRSyncIssues, req).Want(http.StatusOK).JSON(&second)
	if len(second.Issues) != 1 || second.NextCursor != "" {
		t.Fatalf("second page: %+v", second)
	}
	for _, issue := range first.Issues {
		if issue.IssueID == second.Issues[0].IssueID {
			t.Fatal("cursor repeated issue")
		}
	}
}
