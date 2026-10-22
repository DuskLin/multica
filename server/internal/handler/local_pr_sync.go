package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/localgithub"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const localPRSyncInterval = 30 * time.Second

// Local CLI mirrors have no GitHub App installation. Preserve real installation
// IDs when refreshing an existing mirror so App-driven snapshots keep working.
const localPRInstallationID int64 = 0

type localPRSync struct {
	workspaceID pgtype.UUID
	fetch       func(context.Context, localgithub.Reference) (localgithub.PullRequest, error)
}

// ConfigureLocalPRSync opts one workspace into local gh authentication. Only a
// loopback API without a cloud connection may use the server operator's login.
func (h *Handler) ConfigureLocalPRSync(workspaceID, executable string) error {
	publicURL, err := url.Parse(h.cfg.PublicURL)
	if err != nil || h.cfg.CloudURL != "" || publicURL == nil ||
		(publicURL.Hostname() != "localhost" && publicURL.Hostname() != "127.0.0.1" && publicURL.Hostname() != "::1") {
		return fmt.Errorf("local PR sync requires a loopback MULTICA_PUBLIC_URL and no cloud connection")
	}
	id, err := util.ParseUUID(workspaceID)
	if err != nil {
		return fmt.Errorf("local PR sync workspace: %w", err)
	}
	if executable == "" {
		executable = "gh"
	}
	path, err := exec.LookPath(executable)
	if err != nil {
		return fmt.Errorf("local PR sync executable: %w", err)
	}
	h.localPRSync = &localPRSync{workspaceID: id, fetch: (localgithub.Client{Executable: path}).Fetch}
	return nil
}

// StartLocalPRSync polls explicit PR deliveries until shutdown. It is inert
// unless configured; no GitHub login or executable is inspected by default.
func (h *Handler) StartLocalPRSync(ctx context.Context) {
	if h.localPRSync == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(localPRSyncInterval)
		defer ticker.Stop()
		for {
			if err := h.syncLocalPullRequests(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("local PR sync failed", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// syncLocalPullRequests collects deliveries across every nested repository. An
// unfinished agent run delays linking so an intermediate result cannot complete
// the issue before the remaining repositories have delivered their PRs.
func (h *Handler) syncLocalPullRequests(ctx context.Context) error {
	if h.localPRSync == nil {
		return nil
	}
	ws, err := h.Queries.GetWorkspace(ctx, h.localPRSync.workspaceID)
	if err != nil {
		return err
	}
	autoLink, err := autoLinkPRsEnabledForWorkspace(ws)
	if err != nil || !githubFeaturesEnabled(ws) || !autoLink {
		return err
	}
	issues, err := h.Queries.ListLocalPRSyncIssues(ctx, db.ListLocalPRSyncIssuesParams{WorkspaceID: ws.ID})
	if err != nil {
		return err
	}
	var failures []error
	for _, issue := range issues {
		if issue.Unfinished {
			continue
		}
		refs := localgithub.Extract(issue.Delivery)
		if len(refs) == 0 {
			continue
		}
		prs := make([]localgithub.PullRequest, 0, len(refs))
		for _, ref := range refs {
			pr, fetchErr := h.localPRSync.fetch(ctx, ref)
			if fetchErr != nil {
				failures = append(failures, fetchErr)
				break
			}
			prs = append(prs, pr)
		}
		// Fail the entire delivery closed when any repository cannot be read.
		if len(prs) != len(refs) {
			continue
		}
		if err := h.applyPRDelivery(ctx, ws.ID, issue, refs, prs, ""); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// applyPRDelivery commits all links together, then evaluates the existing
// all-linked-PRs merge rule once. Unchanged merged PRs never re-close a user-
// reopened issue, and manually excluded links are not restored by polling.
func (h *Handler) applyPRDelivery(ctx context.Context, workspaceID pgtype.UUID, snapshot db.ListLocalPRSyncIssuesRow, refs []localgithub.Reference, prs []localgithub.PullRequest, daemonID string) error {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	queries := h.Queries.WithTx(tx)
	issue, err := queries.LockLocalPRSyncIssue(ctx, db.LockLocalPRSyncIssueParams{ID: snapshot.ID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	if issue.Revision != snapshot.Revision {
		if daemonID != "" {
			return errPRSyncSnapshotChanged
		}
		return nil
	}
	taskState, err := queries.GetLocalPRSyncTaskState(ctx, db.GetLocalPRSyncTaskStateParams{IssueID: issue.ID, WorkspaceID: workspaceID})
	if err != nil || taskState.Unfinished || taskState.TaskCount != snapshot.TaskCount {
		if err == nil && daemonID != "" {
			return errPRSyncSnapshotChanged
		}
		return err
	}
	if daemonID != "" {
		// Recheck resource ownership and delivered URLs inside the write transaction.
		current, err := queries.ListLocalPRSyncIssues(ctx, db.ListLocalPRSyncIssuesParams{
			WorkspaceID: workspaceID, IssueID: issue.ID,
			DaemonID: pgtype.Text{String: daemonID, Valid: true},
		})
		if err != nil {
			return err
		}
		if len(current) != 1 || current[0].Delivery != snapshot.Delivery {
			return errPRSyncSnapshotChanged
		}
	}
	// Capture merge readiness from the sweep snapshot: a PR shared by several
	// issues may already have been refreshed while processing another issue.
	completionEvent, changed := snapshot.HadUnmergedPr, false
	mergedPRs := make([]pgtype.UUID, 0)
	for index, incoming := range prs {
		ref := refs[index]
		previous, err := queries.GetGitHubPullRequest(ctx, db.GetGitHubPullRequestParams{
			WorkspaceID: workspaceID, RepoOwner: ref.Owner, RepoName: ref.Repo, PrNumber: ref.Number,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		installationID := localPRInstallationID
		if previous.ID.Valid {
			installationID = previous.InstallationID
		}
		pr, err := queries.UpsertGitHubPullRequest(ctx, db.UpsertGitHubPullRequestParams{
			WorkspaceID: workspaceID, InstallationID: installationID,
			RepoOwner: ref.Owner, RepoName: ref.Repo, PrNumber: ref.Number,
			Title: incoming.Title, State: incoming.Status(), HtmlUrl: incoming.URL,
			Branch: ptrToText(strPtrOrNil(incoming.Head.Ref)), HeadSha: incoming.Head.SHA,
			AuthorLogin: ptrToText(strPtrOrNil(incoming.User.Login)), AuthorAvatarUrl: ptrToText(strPtrOrNil(incoming.User.AvatarURL)),
			MergedAt: localPRTime(incoming.MergedAt), ClosedAt: localPRTime(incoming.ClosedAt),
			PrCreatedAt: pgtype.Timestamptz{Time: incoming.CreatedAt, Valid: true},
			PrUpdatedAt: pgtype.Timestamptz{Time: incoming.UpdatedAt, Valid: true},
			Additions:   incoming.Additions, Deletions: incoming.Deletions, ChangedFiles: incoming.ChangedFiles,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			pr = previous // A newer webhook mirror must win over a stale CLI read.
		} else if err != nil {
			return err
		}
		if previous.State != "merged" && pr.State == "merged" {
			mergedPRs = append(mergedPRs, pr.ID)
		}
		excluded, err := queries.IsPullRequestExcludedFromIssue(ctx, db.IsPullRequestExcludedFromIssueParams{IssueID: issue.ID, PullRequestID: pr.ID})
		if err != nil {
			return err
		}
		if excluded {
			continue
		}
		linked, err := queries.LinkIssueToPullRequest(ctx, db.LinkIssueToPullRequestParams{IssueID: issue.ID, PullRequestID: pr.ID})
		if err != nil {
			return err
		}
		completionEvent = completionEvent || linked > 0 || (previous.State != "merged" && pr.State == "merged")
		changed = changed || linked > 0 || !previous.PrUpdatedAt.Time.Equal(pr.PrUpdatedAt.Time) || previous.State != pr.State
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if changed {
		h.publish(protocol.EventPullRequestUpdated, uuidToString(workspaceID), "system", "", struct {
			LinkedIssueIDs []string `json:"linked_issue_ids"`
		}{LinkedIssueIDs: []string{uuidToString(issue.ID)}})
	}
	if completionEvent {
		h.maybeAutoCompleteIssue(ctx, workspaceID, issue.ID, issuestatus.NewResolver(workspaceID))
	}
	// Separate daemon reports may share a PR. Its first merge refresh must
	// also reach other ready deliveries before their own snapshot sees "merged".
	for _, prID := range mergedPRs {
		linkedIssues, err := h.Queries.ListIssueIDsForPullRequest(ctx, prID)
		if err != nil {
			return err
		}
		for _, linkedIssueID := range linkedIssues {
			if linkedIssueID == issue.ID {
				continue
			}
			state, err := h.Queries.GetLocalPRSyncTaskState(ctx, db.GetLocalPRSyncTaskStateParams{IssueID: linkedIssueID, WorkspaceID: workspaceID})
			if err != nil {
				return err
			}
			if !state.Unfinished {
				h.maybeAutoCompleteIssue(ctx, workspaceID, linkedIssueID, issuestatus.NewResolver(workspaceID))
			}
		}
	}
	return nil
}

// localPRTime converts an optional GitHub timestamp to a nullable DB value.
func localPRTime(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}
