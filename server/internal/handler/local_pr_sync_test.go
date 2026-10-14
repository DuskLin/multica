package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/integrations/localgithub"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestLocalPRSync exercises the real DB and completion rule with fake GitHub reads.
func TestLocalPRSync(t *testing.T) {
	for _, scenario := range []string{"all merged", "open", "draft", "closed", "unreadable", "unfinished", "delivery changed", "excluded", "reopened"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			setWorkspacePRMergeStatusForTest(t, "done")
			projectID := dbfx.Insert(t, "project", testutil.Cols{"workspace_id": testWorkspaceID, "title": "Nested repositories"})
			dbfx.Insert(t, "project_resource", testutil.Cols{
				"workspace_id": testWorkspaceID, "project_id": projectID, "resource_type": "local_directory",
				"resource_ref": json.RawMessage(`{"path":"/test/non-git-parent"}`),
			})
			issueID := dbfx.Issue(t, "Multi-repo delivery", testutil.Cols{"status": "in_progress", "project_id": projectID})
			// Unique repository names isolate mirrors across subtests without real GitHub.
			owner := "test-" + issueID
			serviceURL := fmt.Sprintf("https://github.com/%s/service/pull/42", owner)
			webURL := fmt.Sprintf("https://github.com/%s/web/pull/42", owner)
			sharedIssueID := ""
			if scenario == "open" {
				sharedIssueID = dbfx.Issue(t, "Shared PR delivery", testutil.Cols{
					"status": "in_progress", "project_id": projectID, "description": serviceURL + "\n" + webURL,
				})
				dbfx.Cleanup(t, "DELETE FROM issue_pull_request WHERE issue_id = $1", sharedIssueID)
			}
			agentID := dbfx.Agent(t, "Fake delivery agent", "")
			result, err := json.Marshal(struct {
				Output string `json:"output"`
			}{Output: serviceURL + "\n" + webURL})
			if err != nil {
				t.Fatal(err)
			}
			dbfx.Task(t, agentID, testutil.Cols{"runtime_id": testRuntimeID, "issue_id": issueID, "status": "completed", "result": result})
			dbfx.Cleanup(t, "DELETE FROM github_pull_request WHERE workspace_id = $1 AND repo_owner = $2", testWorkspaceID, owner)
			dbfx.Cleanup(t, "DELETE FROM issue_pull_request_exclusion WHERE issue_id = $1", issueID)
			dbfx.Cleanup(t, "DELETE FROM issue_pull_request WHERE issue_id = $1", issueID)
			h := *testHandler
			webState, failed := scenario, scenario == "unreadable"
			if scenario == "unfinished" {
				dbfx.Task(t, agentID, testutil.Cols{"runtime_id": testRuntimeID, "issue_id": issueID, "status": "queued"})
			}
			updatedAt := time.Now().UTC()
			changedDelivery := false
			h.localPRSync = &localPRSync{workspaceID: parseUUID(testWorkspaceID), fetch: func(_ context.Context, ref localgithub.Reference) (localgithub.PullRequest, error) {
				if scenario == "delivery changed" && !changedDelivery {
					changedDelivery = true
					dbfx.Task(t, agentID, testutil.Cols{"runtime_id": testRuntimeID, "issue_id": issueID, "status": "completed",
						"result": json.RawMessage(fmt.Sprintf(`{"output":"https://github.com/%s/schema/pull/42"}`, owner)),
					})
				}
				if ref.Repo == "web" && failed {
					return localgithub.PullRequest{}, errors.New("fake repository permission failure")
				}
				pr := localgithub.PullRequest{Number: ref.Number, URL: ref.URL, Title: ref.Repo, State: "closed", MergedAt: &updatedAt, CreatedAt: updatedAt.Add(-time.Hour), UpdatedAt: updatedAt}
				if ref.Repo == "web" && (webState == "open" || webState == "draft" || webState == "closed") {
					pr.MergedAt = nil
					pr.State = "open"
					pr.Draft = webState == "draft"
					if webState == "closed" {
						pr.State = "closed"
					}
				}
				return pr, nil
			}}
			err = h.syncLocalPullRequests(ctx)
			if (err != nil) != failed {
				t.Fatalf("sync error = %v, failed = %v", err, failed)
			}
			wantCount, wantStatus := 2, "in_progress"
			if scenario == "unfinished" || scenario == "delivery changed" || failed {
				wantCount = 0
			}
			if scenario == "all merged" || scenario == "excluded" || scenario == "reopened" {
				wantStatus = "done"
			}
			if got := linkedPRCountForTest(t, issueID); got != wantCount {
				t.Fatalf("linked PRs = %d, want %d", got, wantCount)
			}
			if got := issueStatusForTest(t, issueID); got != wantStatus {
				t.Fatalf("status = %s, want %s", got, wantStatus)
			}
			if scenario == "delivery changed" {
				if err := h.syncLocalPullRequests(ctx); err != nil || linkedPRCountForTest(t, issueID) != 3 || issueStatusForTest(t, issueID) != "done" {
					t.Fatalf("changed delivery was not retried as a complete batch: %v", err)
				}
			}
			if scenario == "open" || scenario == "draft" || scenario == "closed" || failed {
				webState, failed = "all merged", false
				updatedAt = updatedAt.Add(time.Minute)
				if err := h.syncLocalPullRequests(ctx); err != nil {
					t.Fatal(err)
				}
				if got := issueStatusForTest(t, issueID); got != "done" || linkedPRCountForTest(t, issueID) != 2 {
					t.Fatalf("after final merge: status = %s, links = %d", got, linkedPRCountForTest(t, issueID))
				}
				if sharedIssueID != "" && issueStatusForTest(t, sharedIssueID) != "done" {
					t.Fatal("shared PR merge did not complete the other linked issue")
				}
			}
			if scenario == "excluded" {
				pr, err := h.Queries.GetGitHubPullRequest(ctx, db.GetGitHubPullRequestParams{WorkspaceID: parseUUID(testWorkspaceID), RepoOwner: owner, RepoName: "web", PrNumber: 42})
				if err != nil {
					t.Fatal(err)
				}
				dbfx.Exec(t, "DELETE FROM issue_pull_request WHERE issue_id = $1 AND pull_request_id = $2", issueID, pr.ID)
				if err := h.Queries.ExcludePullRequestFromIssue(ctx, db.ExcludePullRequestFromIssueParams{WorkspaceID: parseUUID(testWorkspaceID), IssueID: parseUUID(issueID), PullRequestID: pr.ID}); err != nil {
					t.Fatal(err)
				}
				if err := h.syncLocalPullRequests(ctx); err != nil || linkedPRCountForTest(t, issueID) != 1 {
					t.Fatalf("excluded link restored: %v", err)
				}
			}
			if scenario == "reopened" {
				dbfx.Exec(t, "UPDATE issue SET status = 'in_progress' WHERE id = $1", issueID)
				if err := h.syncLocalPullRequests(ctx); err != nil || issueStatusForTest(t, issueID) != "in_progress" {
					t.Fatalf("unchanged merge reclosed the issue: %v", err)
				}
			}
		})
	}
}

// TestLocalPRSyncDisabled proves the default path never reads CLI credentials.
func TestLocalPRSyncDisabled(t *testing.T) {
	h := *testHandler
	h.localPRSync = nil
	if err := h.syncLocalPullRequests(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.cfg.PublicURL = "https://multica.ai"
	if err := h.ConfigureLocalPRSync(testWorkspaceID, "/missing/fake-gh"); err == nil {
		t.Fatal("non-local deployment accepted local CLI authentication")
	}
}
