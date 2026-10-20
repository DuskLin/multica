package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// workdirTestRequest exercises the authenticated boundary without opening the
// actual desktop daemon or touching any existing workspace.
func workdirTestRequest(t *testing.T, d *Daemon, action, id, token, origin string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(WorkdirRequest{Profile: d.cfg.Profile, Root: d.cfg.WorkspacesRoot, Action: action, ID: id})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/workdirs", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response := httptest.NewRecorder()
	d.workdirsHandler()(response, request)
	return response
}

func TestWorkdirManualDeletionProtectsRunningAndUnpublishedWork(t *testing.T) {
	for _, scenario := range []string{"unfinished", "running", "dirty", "unregistered", "origin", "wrong-token", "busy"} {
		t.Run(scenario, func(t *testing.T) {
			d := newGCTestDaemon(t, http.NotFoundHandler())
			params, path := scriptGCDaemonFixture(t, d)
			token, origin, id := "test-token", "", workdirID(path)
			switch scenario {
			case "running":
				release, err := execenv.ClaimLocalScriptWorkspace(params)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			case "dirty":
				if err := os.WriteFile(filepath.Join(path, "result.txt"), []byte("not published"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unregistered":
				id = workdirID(t.TempDir())
			case "origin":
				origin = "https://example.invalid"
			case "wrong-token":
				token = "wrong"
			case "busy":
				d.workdirOperations.Lock()
				defer d.workdirOperations.Unlock()
			}
			response := workdirTestRequest(t, d, "delete", id, token, origin)
			if scenario == "unfinished" {
				if response.Code != http.StatusOK {
					t.Fatalf("delete: %d %s", response.Code, response.Body)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("directory still exists: %v", err)
				}
				manifest := filepath.Join(filepath.Dir(path), ".multica-script", filepath.Base(path)+".recovery.json")
				if _, err := os.Stat(manifest); err != nil {
					t.Fatalf("recovery record missing: %v", err)
				}
			} else {
				if response.Code == http.StatusOK {
					t.Fatalf("unsafe deletion accepted: %s", response.Body)
				}
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("directory was changed: %v", err)
				}
			}
		})
	}
}

func TestWorkdirListIncludesBothKindsAndResolvesIssueIdentity(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/issues/issue-a", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"id": "issue-a", "workspace_id": "ws-a", "title": "Preserve branch", "status": "in_progress"})
	})
	d := newGCTestDaemon(t, mux)
	normal := createTaskDir(t, d.cfg.WorkspacesRoot, "ws-a", "task-a", &execenv.GCMeta{WorkspaceID: "ws-a", IssueID: "issue-a", Kind: execenv.GCKindIssue})
	_, script := scriptGCDaemonFixture(t, d)
	d.markActiveEnvRoot(normal)
	defer d.unmarkActiveEnvRoot(normal)
	response := workdirTestRequest(t, d, "list", "", "test-token", "")
	if response.Code != http.StatusOK {
		t.Fatalf("list: %d %s", response.Code, response.Body)
	}
	var report WorkdirReport
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Directories) != 2 {
		t.Fatalf("directories: %+v", report.Directories)
	}
	for _, row := range report.Directories {
		if row.Path == normal && (!row.Active || row.ParentTitle != "Preserve branch" || row.ParentStatus != "in_progress") {
			t.Fatalf("wrong metadata: %+v", row)
		}
		if row.Path == script && row.Mode != "script" {
			t.Fatalf("script mode lost: %+v", row)
		}
	}
}

func TestWorkdirDeletionRejectsOutsideRootAndPreservesOutputs(t *testing.T) {
	d := newGCTestDaemon(t, http.NotFoundHandler())
	path := createTaskDir(t, d.cfg.WorkspacesRoot, "ws-a", "task-a", &execenv.GCMeta{WorkspaceID: "ws-a", IssueID: "issue-a"})
	if err := os.MkdirAll(filepath.Join(path, "output"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "output", "result.txt"), []byte("local result"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.deleteWorkDirectory(context.Background(), workdirID(path)); err == nil || !strings.Contains(err.Error(), "result.txt") {
		t.Fatalf("expected protected output: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, ".task_owner")); err != nil {
		t.Fatalf("owner invalidated before safety checks: %v", err)
	}
}

func TestWorkdirReclaimRunsExistingDonePolicy(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/daemon/workspaces/script-workspace/issues/gc-check", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string][]IssueGCCheckResult{"issues": {{ID: "script-issue", Found: true, Status: "done", Category: "done"}}})
	})
	d := newGCTestDaemon(t, mux)
	_, path := scriptGCDaemonFixture(t, d)
	response := workdirTestRequest(t, d, "reclaim", "", "test-token", "")
	if response.Code != http.StatusOK {
		t.Fatalf("reclaim: %d %s", response.Code, response.Body)
	}
	var result WorkdirActionResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Removed != 1 || result.ReclaimedBytes <= 0 {
		t.Fatalf("wrong summary: %+v", result)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("completed directory still exists: %v", err)
	}
}

func TestWorkdirEndpointRefusesWrongProfileRoot(t *testing.T) {
	d := newGCTestDaemon(t, http.NotFoundHandler())
	request := httptest.NewRequest(http.MethodPost, "/workdirs", strings.NewReader(`{"profile":"another-profile","root":"/outside","action":"reclaim","id":""}`))
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	d.workdirsHandler()(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("wrong profile accepted: %d", response.Code)
	}
}

// TestWorkdirNavigation verifies identities, workspace headers, and unavailable
// targets using authenticated HTTP responses and isolated local provenance.
func TestWorkdirNavigation(t *testing.T) {
	root := t.TempDir()
	if err := execenv.WriteManagedEnvProvenance(root, execenv.ManagedEnvProvenance{WorkspaceID: "ws-a", IssueID: "issue-a"}); err != nil {
		t.Fatal(err)
	}
	requests := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing authentication")
		}
		requests[r.URL.Path]++
		switch r.URL.Path {
		case "/api/workspaces":
			json.NewEncoder(w).Encode([]WorkspaceInfo{{ID: "ws-a", Name: "Team", Slug: "team-a"}})
		case "/api/issues/issue-a", "/api/chat/sessions/chat-a", "/api/issues/wrong-workspace", "/api/issues/wrong-id":
			if r.Header.Get("X-Workspace-ID") != "ws-a" {
				t.Error("resource read was not workspace scoped")
			}
			id := filepath.Base(r.URL.Path)
			workspace := "ws-a"
			if id == "wrong-workspace" {
				workspace = "ws-b"
			}
			if id == "wrong-id" {
				id = "another-issue"
			}
			json.NewEncoder(w).Encode(workdirResource{ID: id, WorkspaceID: workspace, Title: "Task title", Status: "done"})
		case "/api/daemon/tasks/run-a/gc-check", "/api/daemon/tasks/run-b/gc-check":
			json.NewEncoder(w).Encode(TaskGCStatus{Status: "failed"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewClient(server.URL)
	client.SetToken("test-token")
	report := WorkdirReport{Directories: []WorkDirectory{
		{WorkspaceID: "ws-a", Kind: "issue", ParentID: "issue-a"},
		{WorkspaceID: "ws-a", Kind: "chat", ParentID: "chat-a"},
		{WorkspaceID: "ws-a", Kind: "run", ParentID: "run-a", Mode: "managed", Path: root},
		{WorkspaceID: "ws-a", Kind: "run", ParentID: "run-b", Mode: "managed", Path: t.TempDir()},
		{WorkspaceID: "ws-a", Kind: "issue", ParentID: "deleted"},
		{WorkspaceID: "ws-a", Kind: "issue", ParentID: "wrong-workspace"},
		{WorkspaceID: "ws-a", Kind: "issue", ParentID: "wrong-id"},
	}}
	ResolveWorkDirectoryTasks(context.Background(), &report, client)
	if report.ServerURL != server.URL {
		t.Fatalf("missing server identity: %+v", report)
	}
	for index, row := range report.Directories {
		if row.WorkspaceSlug != "team-a" {
			t.Fatalf("missing workspace route: %+v", row)
		}
		if index < 3 {
			wantKind, wantID := "issue", "issue-a"
			if index == 1 {
				wantKind, wantID = "chat", "chat-a"
			}
			if row.TargetKind != wantKind || row.TargetID != wantID || row.ParentTitle != "Task title" {
				t.Fatalf("wrong target: %+v", row)
			}
		} else if row.TargetID != "" || row.TargetKind != "" {
			t.Fatalf("unsafe target: %+v", row)
		}
	}
	if report.Directories[2].ParentStatus != "failed" {
		t.Fatal("run status replaced by parent status")
	}
	if requests["/api/issues/issue-a"] != 1 {
		t.Fatal("shared parent should be resolved once")
	}
}
