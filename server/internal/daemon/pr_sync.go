package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/integrations/localgithub"
	"github.com/multica-ai/multica/server/internal/util"
)

const (
	prSyncInterval    = 30 * time.Second
	prSyncSweepBudget = 5 * time.Minute
)

// resolvePRSyncConfig applies explicit environment overrides without inspecting gh.
func resolvePRSyncConfig(workspaceID, executable string) (string, string, error) {
	if value, exists := os.LookupEnv("MULTICA_DAEMON_PR_SYNC_WORKSPACE_ID"); exists {
		workspaceID = value
	}
	if value, exists := os.LookupEnv("MULTICA_DAEMON_PR_SYNC_GH_PATH"); exists {
		executable = value
	}
	workspaceID, executable = strings.TrimSpace(workspaceID), strings.TrimSpace(executable)
	if workspaceID != "" {
		id, err := util.ParseUUID(workspaceID)
		if err != nil {
			return "", "", fmt.Errorf("invalid PR sync workspace ID: %w", err)
		}
		workspaceID = util.UUIDToString(id)
	}
	if executable == "" {
		executable = "gh"
	}
	return workspaceID, executable, nil
}

// prSyncLoop refreshes deliveries independently of agent execution and survives restarts.
func (d *Daemon) prSyncLoop(ctx context.Context) {
	if d.cfg.PRSyncWorkspaceID == "" {
		return
	}
	ticker := time.NewTicker(prSyncInterval)
	defer ticker.Stop()
	cursor := ""
	for {
		runtimeID := d.prSyncRuntimeID()
		if runtimeID != "" {
			d.setPRSyncPhase(prSyncSyncing)
			err := d.syncPullRequests(ctx, runtimeID, &cursor)
			if err != nil && ctx.Err() == nil {
				d.setPRSyncPhase(prSyncError)
				d.logger.Warn("daemon PR sync failed", "workspace_id", d.cfg.PRSyncWorkspaceID, "error", err)
			} else if ctx.Err() == nil {
				d.setPRSyncPhase(prSyncIdle)
			}
		} else {
			d.setPRSyncPhase(prSyncWaiting)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// prSyncRuntimeID selects only an already registered runtime in the opted-in workspace.
func (d *Daemon) prSyncRuntimeID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws := d.workspaces[d.cfg.PRSyncWorkspaceID]
	if ws == nil || len(ws.runtimeIDs) == 0 {
		return ""
	}
	return ws.runtimeIDs[0]
}

// syncPullRequests never starts gh before receiving a validated, scoped delivery.
func (d *Daemon) syncPullRequests(ctx context.Context, runtimeID string, cursor *string) error {
	return syncPRDeliveries(ctx, d.client, runtimeID, cursor, func(ctx context.Context, ref localgithub.Reference) (localgithub.PullRequest, error) {
		path, err := exec.LookPath(d.cfg.PRSyncGHPath)
		if err != nil {
			return localgithub.PullRequest{}, fmt.Errorf("PR sync requires a logged-in GitHub CLI: %w", err)
		}
		return (localgithub.Client{Executable: path}).Fetch(ctx, ref)
	})
}

// syncPRDeliveries uploads complete deliveries and caches shared PR reads for one sweep.
func syncPRDeliveries(ctx context.Context, client *Client, runtimeID string, cursor *string, fetch func(context.Context, localgithub.Reference) (localgithub.PullRequest, error)) error {
	started := time.Now()
	seenCursors := make(map[string]bool)
	cache := make(map[string]localgithub.PullRequest)
	var failures []error
	for {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if time.Since(started) >= prSyncSweepBudget {
			return errors.Join(failures...)
		}
		page, err := client.listPRSyncIssues(ctx, runtimeID, *cursor)
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		for _, issue := range page.Issues {
			// Yield between complete deliveries and resume after the last issue,
			// so a large history cannot starve newer tasks on every timed sweep.
			if time.Since(started) >= prSyncSweepBudget {
				return errors.Join(failures...)
			}
			report := localgithub.SyncReport{Issue: issue}
			for _, ref := range issue.References {
				key := strings.ToLower(ref.URL)
				pr, exists := cache[key]
				if !exists {
					pr, err = fetch(ctx, ref)
					if err != nil {
						failures = append(failures, err)
						break
					}
					cache[key] = pr
				}
				report.PullRequests = append(report.PullRequests, pr)
			}
			if ctx.Err() != nil {
				return errors.Join(append(failures, ctx.Err())...)
			}
			*cursor = issue.IssueID
			if len(report.PullRequests) != len(issue.References) {
				continue
			}
			if err := client.reportPRSync(ctx, runtimeID, report); err != nil {
				failures = append(failures, err)
			}
		}
		if page.NextCursor == "" {
			*cursor = ""
			return errors.Join(failures...)
		}
		if seenCursors[page.NextCursor] {
			return errors.New("PR sync returned a repeated pagination cursor")
		}
		seenCursors[page.NextCursor] = true
		*cursor = page.NextCursor
	}
}

// listPRSyncIssues validates the wire contract before any local credential is used.
func (c *Client) listPRSyncIssues(ctx context.Context, runtimeID, cursor string) (localgithub.SyncPage, error) {
	var page localgithub.SyncPage
	path := "/api/daemon/runtimes/" + url.PathEscape(runtimeID) + "/pull-requests/sync?after=" + url.QueryEscape(cursor)
	if err := c.getJSON(ctx, path, &page); err != nil {
		return page, err
	}
	if err := validatePRSyncPage(page); err != nil {
		return localgithub.SyncPage{}, err
	}
	return page, nil
}

// reportPRSync submits metadata through the daemon's existing authenticated client.
func (c *Client) reportPRSync(ctx context.Context, runtimeID string, report localgithub.SyncReport) error {
	return c.postJSON(ctx, "/api/daemon/runtimes/"+url.PathEscape(runtimeID)+"/pull-requests/sync", report, nil)
}

// validatePRSyncPage rejects malformed, oversized, duplicate, or non-GitHub references.
func validatePRSyncPage(page localgithub.SyncPage) error {
	if page.Issues == nil || len(page.Issues) > localgithub.SyncPageSize {
		return errors.New("invalid PR sync issue list")
	}
	if page.NextCursor != "" {
		if _, err := util.ParseUUID(page.NextCursor); err != nil {
			return errors.New("invalid PR sync cursor")
		}
	}
	seen := make(map[string]bool)
	for _, issue := range page.Issues {
		if _, err := util.ParseUUID(issue.IssueID); err != nil || seen[issue.IssueID] ||
			issue.Revision < 0 || issue.TaskCount < 1 || len(issue.References) == 0 || len(issue.References) > localgithub.MaxSyncPullRequests {
			return errors.New("invalid PR sync delivery")
		}
		seen[issue.IssueID] = true
		var urls []string
		for _, ref := range issue.References {
			urls = append(urls, ref.URL)
		}
		if !slices.Equal(localgithub.Extract(strings.Join(urls, "\n")), issue.References) {
			return errors.New("invalid PR sync references")
		}
	}
	return nil
}

// prSyncPhase is the finite local diagnostic state of the synchronization loop.
type prSyncPhase string

const (
	prSyncDisabled prSyncPhase = "disabled"
	prSyncWaiting  prSyncPhase = "waiting"
	prSyncSyncing  prSyncPhase = "syncing"
	prSyncIdle     prSyncPhase = "idle"
	prSyncError    prSyncPhase = "error"
)

// prSyncHealth exposes effective configuration and bounded, credential-free diagnostics.
type prSyncHealth struct {
	WorkspaceID        string      `json:"workspace_id"`
	Phase              prSyncPhase `json:"phase"`
	EnvironmentManaged bool        `json:"environment_managed"`
}

// setPRSyncPhase publishes a loop transition for local settings consumers.
func (d *Daemon) setPRSyncPhase(phase prSyncPhase) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prSyncPhase = phase
}

// prSyncSnapshot keeps disabled and not-yet-started loops distinct from success.
func (d *Daemon) prSyncSnapshot() prSyncHealth {
	d.mu.Lock()
	defer d.mu.Unlock()
	phase := d.prSyncPhase
	if d.cfg.PRSyncWorkspaceID == "" {
		phase = prSyncDisabled
	} else if phase == "" {
		phase = prSyncWaiting
	}
	_, managed := os.LookupEnv("MULTICA_DAEMON_PR_SYNC_WORKSPACE_ID")
	return prSyncHealth{WorkspaceID: d.cfg.PRSyncWorkspaceID, Phase: phase, EnvironmentManaged: managed}
}

// prSyncRestartHandler applies saved settings only after atomically fencing new claims.
func (d *Daemon) prSyncRestartHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !d.authorizeLocalControl(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-Multica-Profile") != d.cfg.Profile {
			http.Error(w, "profile mismatch", http.StatusConflict)
			return
		}
		if !d.ready.Load() || !d.updating.CompareAndSwap(false, true) {
			http.Error(w, "daemon is not ready to restart", http.StatusConflict)
			return
		}
		if !d.trySetClaimBarrier() {
			d.updating.Store(false)
			http.Error(w, "wait for active runs and claims to finish", http.StatusConflict)
			return
		}
		if _, err := d.restartTargetBinary(); err != nil {
			d.releaseClaimBarrier()
			d.updating.Store(false)
			http.Error(w, "restart binary unavailable", http.StatusServiceUnavailable)
			return
		}
		// Flush acceptance before cancellation closes the health server connection.
		w.WriteHeader(http.StatusAccepted)
		if err := http.NewResponseController(w).Flush(); err != nil {
			d.releaseClaimBarrier()
			d.updating.Store(false)
			return
		}
		if !d.triggerRestart() {
			d.releaseClaimBarrier()
			d.updating.Store(false)
		}
	}
}
