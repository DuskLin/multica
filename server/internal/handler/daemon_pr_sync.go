package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/localgithub"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const maxPRSyncReportBytes = 2 << 20

var errPRSyncSnapshotChanged = errors.New("PR delivery changed; fetch a new sync snapshot")

// requirePRSyncRuntime pins reports to the caller's registered machine and workspace.
func (h *Handler) requirePRSyncRuntime(w http.ResponseWriter, r *http.Request) (db.AgentRuntime, bool) {
	rt, ok := h.requireDaemonRuntimeAccess(w, r, chi.URLParam(r, "runtimeId"))
	if !ok {
		return db.AgentRuntime{}, false
	}
	identity := middleware.DaemonIDFromContext(r.Context())
	if !rt.DaemonID.Valid || rt.DaemonID.String == "" ||
		(identity != "" && identity != rt.DaemonID.String) ||
		(identity == "" && (!rt.OwnerID.Valid || uuidToString(rt.OwnerID) != requestUserID(r))) {
		writeError(w, http.StatusNotFound, "runtime not found")
		return db.AgentRuntime{}, false
	}
	ws, err := h.Queries.GetWorkspace(r.Context(), rt.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load workspace")
		return db.AgentRuntime{}, false
	}
	enabled, err := autoLinkPRsEnabledForWorkspace(ws)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read PR sync settings")
		return db.AgentRuntime{}, false
	}
	if !enabled {
		writeError(w, http.StatusForbidden, "automatic PR linking is disabled")
		return db.AgentRuntime{}, false
	}
	return rt, true
}

// ListDaemonPRSyncIssues returns deliveries from this machine's local projects.
func (h *Handler) ListDaemonPRSyncIssues(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.requirePRSyncRuntime(w, r)
	if !ok {
		return
	}
	var after pgtype.UUID
	if cursor := r.URL.Query().Get("after"); cursor != "" {
		after, ok = parseUUIDOrBadRequest(w, cursor, "after")
		if !ok {
			return
		}
	}
	rows, err := h.Queries.ListLocalPRSyncIssues(r.Context(), db.ListLocalPRSyncIssuesParams{
		WorkspaceID: rt.WorkspaceID, DaemonID: rt.DaemonID,
		AfterID: after, MaxIssues: localgithub.SyncPageSize,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list PR sync deliveries")
		return
	}
	page := localgithub.SyncPage{Issues: []localgithub.SyncIssue{}}
	for _, row := range rows {
		refs := localgithub.Extract(row.Delivery)
		if row.Unfinished || len(refs) == 0 {
			continue
		}
		if len(refs) > localgithub.MaxSyncPullRequests {
			slog.Warn("daemon PR sync skipped oversized delivery", "issue_id", uuidToString(row.ID), "pr_count", len(refs))
			continue
		}
		page.Issues = append(page.Issues, localgithub.SyncIssue{
			IssueID: uuidToString(row.ID), Revision: row.Revision, TaskCount: row.TaskCount, References: refs,
		})
	}
	if len(rows) == localgithub.SyncPageSize {
		page.NextCursor = uuidToString(rows[len(rows)-1].ID)
	}
	writeJSON(w, http.StatusOK, page)
}

// ReportDaemonPRSync accepts only a complete, still-current server-selected delivery.
func (h *Handler) ReportDaemonPRSync(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.requirePRSyncRuntime(w, r)
	if !ok {
		return
	}
	var report localgithub.SyncReport
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPRSyncReportBytes))
	if err := decoder.Decode(&report); err != nil {
		writeError(w, http.StatusBadRequest, "invalid PR sync report")
		return
	}
	issueID, ok := parseUUIDOrBadRequest(w, report.Issue.IssueID, "issue_id")
	if !ok {
		return
	}
	rows, err := h.Queries.ListLocalPRSyncIssues(r.Context(), db.ListLocalPRSyncIssuesParams{
		WorkspaceID: rt.WorkspaceID, DaemonID: rt.DaemonID, IssueID: issueID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load PR sync delivery")
		return
	}
	if len(rows) != 1 {
		writeError(w, http.StatusNotFound, "issue delivery not found")
		return
	}
	row := rows[0]
	refs := localgithub.Extract(row.Delivery)
	if row.Unfinished || row.Revision != report.Issue.Revision || row.TaskCount != report.Issue.TaskCount ||
		!slices.Equal(refs, report.Issue.References) {
		writeError(w, http.StatusConflict, errPRSyncSnapshotChanged.Error())
		return
	}
	if len(refs) == 0 || len(refs) > localgithub.MaxSyncPullRequests || len(refs) != len(report.PullRequests) {
		writeError(w, http.StatusBadRequest, "report must contain every delivered PR exactly once")
		return
	}
	for index, pr := range report.PullRequests {
		if err := pr.Validate(refs[index]); err != nil {
			writeError(w, http.StatusBadRequest, "invalid PR metadata or mismatched PR identity")
			return
		}
	}
	if err := h.applyPRDelivery(r.Context(), rt.WorkspaceID, row, refs, report.PullRequests, rt.DaemonID.String); err != nil {
		if errors.Is(err, errPRSyncSnapshotChanged) {
			writeError(w, http.StatusConflict, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "failed to apply PR sync report")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
