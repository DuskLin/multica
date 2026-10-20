package daemon

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

const WorkdirOperationTimeout = 30 * time.Minute
const workdirRequestLimit = 16 << 10

// WorkDirectory is one locally managed directory, with unresolved server data
// represented explicitly rather than mistaken for a completed task.
type WorkDirectory struct {
	ID            string `json:"id"`
	Profile       string `json:"profile"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	WorkspaceSlug string `json:"workspace_slug"`
	TargetKind    string `json:"target_kind"`
	TargetID      string `json:"target_id"`
	ParentID      string `json:"parent_id"`
	ParentTitle   string `json:"parent_title"`
	ParentStatus  string `json:"parent_status"`
	Kind          string `json:"kind"`
	Mode          string `json:"mode"`
	Path          string `json:"path"`
	SizeBytes     int64  `json:"size_bytes"`
	Active        bool   `json:"active"`
	BlockedReason string `json:"blocked_reason"`
}

// WorkdirResult records one skipped/failed operation without hiding partial success.
type WorkdirResult struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// WorkdirReport covers one profile; the desktop command aggregates all profiles.
type WorkdirReport struct {
	Profile     string          `json:"profile"`
	ServerURL   string          `json:"server_url"`
	Root        string          `json:"root"`
	Online      bool            `json:"online"`
	Directories []WorkDirectory `json:"directories"`
	Problems    []WorkdirResult `json:"problems"`
}

// WorkdirActionResult reports completed operations, never optimistic deletion.
type WorkdirActionResult struct {
	Removed        int             `json:"removed"`
	Skipped        int             `json:"skipped"`
	ReclaimedBytes int64           `json:"reclaimed_bytes"`
	Problems       []WorkdirResult `json:"problems"`
}

// WorkdirRequest pins an operation to both a profile and its configured root.
// Clients send an opaque directory ID, never a filesystem path to delete.
type WorkdirRequest struct {
	Profile string `json:"profile"`
	Root    string `json:"root"`
	Action  string `json:"action"`
	ID      string `json:"id"`
}

// workdirID binds user selection to a canonical scan result.
func workdirID(path string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(path)))
	return hex.EncodeToString(digest[:])
}

// ScanWorkDirectories combines ordinary env roots with registered script
// directories. Failed sources are reported individually; they never authorize
// deletion or turn a partially scanned machine into an apparently empty one.
func ScanWorkDirectories(root, profile string) (WorkdirReport, error) {
	report := WorkdirReport{Profile: profile, Root: root, Directories: []WorkDirectory{}, Problems: []WorkdirResult{}}
	disk, err := ScanDiskUsage(root, ArtifactPatternsFromEnv())
	if err != nil {
		return report, err
	}
	for _, row := range disk.Tasks {
		entry := WorkDirectory{ID: workdirID(row.Path), Profile: profile, WorkspaceID: row.WorkspaceID, ParentID: row.ParentID, ParentStatus: row.ParentStatus, Kind: row.Kind, Mode: "managed", Path: row.Path, SizeBytes: row.SizeBytes}
		owner, err := execenv.ReadEnvRootOwner(row.Path)
		if err != nil || owner == nil || owner.TaskID == "" {
			entry.BlockedReason = "Directory ownership cannot be verified"
		}
		if entry.ParentID == "" {
			if provenance, err := execenv.ReadManagedEnvProvenance(row.Path); err == nil && provenance != nil {
				if provenance.IssueID != "" {
					entry.ParentID, entry.Kind = provenance.IssueID, "issue"
				} else if provenance.ChatSessionID != "" {
					entry.ParentID, entry.Kind = provenance.ChatSessionID, "chat"
				}
			}
			if entry.ParentID == "" && owner != nil {
				entry.ParentID, entry.Kind = owner.TaskID, "run"
			}
		}
		report.Directories = append(report.Directories, entry)
	}
	sources, err := execenv.ListLocalScriptSources(root)
	if err != nil {
		report.Problems = append(report.Problems, WorkdirResult{root, err.Error()})
		return report, nil
	}
	seen := map[string]bool{}
	for _, source := range sources {
		directories, err := execenv.ListLocalScriptWorkspaces(source)
		if err != nil {
			report.Problems = append(report.Problems, WorkdirResult{source.LocalPath, err.Error()})
			continue
		}
		for _, params := range directories {
			path, err := params.WorkspacePath()
			if err != nil || seen[path] {
				continue
			}
			if info, err := os.Lstat(path); err != nil || !info.IsDir() {
				continue
			}
			seen[path] = true
			kind, parent := "issue", params.IssueID
			if parent == "" {
				kind, parent = "chat", params.ChatSessionID
			}
			if parent == "" {
				kind, parent = "run", params.TaskID
			}
			entry := WorkDirectory{ID: workdirID(path), Profile: profile, WorkspaceID: params.WorkspaceID, ParentID: parent, Kind: kind, Mode: "script", Path: path, SizeBytes: dirSize(path)}
			release, err := execenv.ClaimLocalScriptWorkspace(params)
			if err != nil {
				entry.BlockedReason = err.Error()
			} else {
				release()
			}
			report.Directories = append(report.Directories, entry)
		}
	}
	return report, nil
}

// workdirResource is the minimum authenticated projection used to build a link.
type workdirResource struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
}

// workdirTarget uses recorded ownership only; directory names are never task IDs.
func workdirTarget(row WorkDirectory) (string, string) {
	if row.Kind == "issue" || row.Kind == "chat" {
		return row.Kind, row.ParentID
	}
	if row.Mode == "managed" && (row.Kind == "run" || row.Kind == "quick_create") {
		owner, err := execenv.ReadManagedEnvProvenance(row.Path)
		if err == nil && owner != nil && owner.ManagedBy == execenv.ManagedEnvProvenanceManagedBy && owner.WorkspaceID == row.WorkspaceID {
			if owner.IssueID != "" {
				return "issue", owner.IssueID
			}
			if owner.ChatSessionID != "" {
				return "chat", owner.ChatSessionID
			}
		}
	}
	return "", ""
}

// ResolveWorkDirectoryTasks adds authenticated names/statuses and verified link
// targets. Missing, inaccessible, or cross-workspace resources never get links.
func ResolveWorkDirectoryTasks(ctx context.Context, report *WorkdirReport, client *Client) {
	if client == nil {
		return
	}
	report.ServerURL = client.baseURL
	// Navigation needs slugs, which the daemon's minimal membership list omits.
	workspaces, err := client.listLegacyWorkspaces(ctx)
	if err != nil {
		report.Problems = append(report.Problems, WorkdirResult{report.Root, "Workspace details unavailable"})
	}
	memberships := map[string]WorkspaceInfo{}
	for _, workspace := range workspaces {
		memberships[workspace.ID] = workspace
	}
	summaries := map[string]workdirResource{}
	for i := range report.Directories {
		row := &report.Directories[i]
		workspace := memberships[row.WorkspaceID]
		row.WorkspaceName, row.WorkspaceSlug = workspace.Name, workspace.Slug
		row.TargetKind, row.TargetID = "", ""
		if row.ParentID == "" {
			continue
		}
		switch row.Kind {
		case "autopilot_run":
			if status, err := client.GetAutopilotRunGCCheck(ctx, row.ParentID); err == nil && status != nil {
				row.ParentStatus = status.Status
			}
		case "run", "quick_create":
			if status, err := client.GetTaskGCCheck(ctx, row.ParentID); err == nil && status != nil {
				row.ParentStatus = status.Status
			}
		}
		kind, id := workdirTarget(*row)
		if id == "" {
			continue
		}
		key := row.WorkspaceID + "/" + kind + "/" + id
		summary, found := summaries[key]
		if !found {
			resourcePath := "/api/issues/" + url.PathEscape(id)
			if kind == "chat" {
				resourcePath = "/api/chat/sessions/" + url.PathEscape(id)
			}
			if err := client.getJSONWithWorkspace(ctx, resourcePath, client.Token(), row.WorkspaceID, &summary); err != nil || summary.WorkspaceID != row.WorkspaceID || summary.ID != id {
				summary = workdirResource{}
			}
			summaries[key] = summary
		}
		row.ParentTitle = summary.Title
		if row.Kind == "issue" || row.Kind == "chat" {
			row.ParentStatus = summary.Status
		}
		if summary.ID != "" && workspace.Slug != "" {
			row.TargetKind, row.TargetID = kind, id
		}
	}
}

// workdirsHandler is a local authenticated management endpoint. Browser origins
// are rejected, and profile/root matching prevents collisions between daemons
// sharing a health port or a token. All mutations use live execution locks.
func (d *Daemon) workdirsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		token := ""
		if d.client != nil {
			token = d.client.Token()
		}
		if token == "" || r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var request WorkdirRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, workdirRequestLimit))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if request.Profile != d.cfg.Profile || request.Root != d.cfg.WorkspacesRoot {
			http.Error(w, "profile or workspace root mismatch", http.StatusConflict)
			return
		}
		if request.Action != "list" && request.Action != "reclaim" && request.Action != "delete" && request.Action != "start-delete" && request.Action != "start-reclaim" && request.Action != "operations" {
			http.Error(w, "unknown action", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), WorkdirOperationTimeout)
		defer cancel()
		w.Header().Set("Content-Type", "application/json")
		if request.Action == "operations" {
			json.NewEncoder(w).Encode(d.workdirJobSnapshot())
			return
		}
		if request.Action == "start-delete" || request.Action == "start-reclaim" {
			if request.Action == "start-delete" {
				decoded, err := hex.DecodeString(request.ID)
				if err != nil || len(decoded) != sha256.Size {
					http.Error(w, "invalid directory ID", http.StatusBadRequest)
					return
				}
			}
			response, err := d.enqueueWorkdirJob(strings.TrimPrefix(request.Action, "start-"), request.ID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			json.NewEncoder(w).Encode(response)
			return
		}
		if request.Action == "list" {
			report, err := ScanWorkDirectories(d.cfg.WorkspacesRoot, d.cfg.Profile)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			report.Online = true
			for i := range report.Directories {
				row := &report.Directories[i]
				if row.Mode == "managed" && d.isActiveEnvRoot(row.Path) {
					row.Active = true
					row.BlockedReason = "Directory is in use by a running task"
				}
			}
			ResolveWorkDirectoryTasks(ctx, &report, d.client)
			json.NewEncoder(w).Encode(report)
			return
		}
		if !d.workdirOperations.TryLock() {
			http.Error(w, "Another cleanup is running", http.StatusConflict)
			return
		}
		defer d.workdirOperations.Unlock()
		result := WorkdirActionResult{Problems: []WorkdirResult{}}
		if request.Action == "reclaim" {
			stats := &gcStats{byPattern: map[string]int{}}
			d.runGCCycle(ctx, stats)
			result = workdirGCResult(stats)
		} else {
			removed, err := d.deleteWorkDirectory(ctx, request.ID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			result.Removed = 1
			result.ReclaimedBytes = removed
		}
		json.NewEncoder(w).Encode(result)
	}
}

// deleteWorkDirectory accepts only a freshly enumerated owned directory. Manual
// deletion ignores issue completion, but never running locks or remote checks.
func (d *Daemon) deleteWorkDirectory(ctx context.Context, id string) (int64, error) {
	row, err := d.findWorkDirectory(ctx, id)
	if err != nil {
		return 0, err
	}
	return d.deleteWorkDirectoryRow(ctx, row)
}

// deleteWorkDirectoryRow measures only the selected directory before validation.
func (d *Daemon) deleteWorkDirectoryRow(ctx context.Context, row WorkDirectory) (int64, error) {
	execenv.ReportWorkdirProgress(ctx, "measuring", row.Path, 0)
	size, err := dirSizeContext(ctx, row.Path)
	if err != nil {
		return 0, err
	}
	row.SizeBytes = size
	if row.BlockedReason != "" {
		return 0, errors.New(row.BlockedReason)
	}
	if row.Mode == "script" {
		params := execenv.LocalScriptParams{LocalPath: filepath.Dir(filepath.Dir(row.Path)), WorkspaceID: row.WorkspaceID}
		switch row.Kind {
		case "issue":
			params.IssueID = row.ParentID
		case "chat":
			params.ChatSessionID = row.ParentID
		case "run":
			params.TaskID = row.ParentID
		default:
			return 0, errors.New("unknown directory owner")
		}
		removed, err := execenv.CollectLocalScriptWorkspace(ctx, params, func(context.Context) (bool, error) { return true, nil })
		if err != nil || !removed {
			return 0, errors.Join(errors.New("directory could not be removed"), err)
		}
	} else {
		release, ok := d.reserveEnvRootForGC(row.Path)
		if !ok {
			return 0, errors.New("Directory is in use by a running task")
		}
		defer release()
		owner, err := d.gcTaskDirOwner(row.Path)
		if err != nil {
			return 0, err
		}
		err = execenv.RemoveManagedWorkDirectory(ctx, d.cfg.WorkspacesRoot, row.Path, func() error {
			current, err := d.gcTaskDirOwner(row.Path)
			if err != nil {
				return err
			}
			if *current != *owner {
				return errors.New("directory owner changed")
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
		if err := execenv.RemoveRootDirRecord(d.cfg.WorkspacesRoot, row.Path, *owner); err != nil {
			d.logger.Warn("workdirs: remove root record", "error", err)
		}
	}
	return row.SizeBytes, nil
}
