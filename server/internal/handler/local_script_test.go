package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestValidateLocalDirectoryScript(t *testing.T) {
	for _, tc := range []struct {
		script any
		valid  bool
	}{
		{"init-workspace.sh", true}, {" /source with spaces/init.sh ", true},
		{"", false}, {"init.js", false}, {123, false},
	} {
		raw, _ := json.Marshal(map[string]any{"local_path": "/source", "daemon_id": "d1", "execution_mode": "script", "init_script": tc.script})
		normalized, err := validateLocalDirectoryRef(raw)
		if (err == nil) != tc.valid {
			t.Fatalf("script=%v: %v", tc.script, err)
		}
		if tc.valid {
			var ref localDirectoryRef
			if err := json.Unmarshal(normalized, &ref); err != nil {
				t.Fatal(err)
			}
			if ref.InitScript != strings.TrimSpace(tc.script.(string)) || ref.ExecutionMode != "script" {
				t.Fatalf("lost script configuration: %+v", ref)
			}
		}
	}
}

func TestScriptClaimGate(t *testing.T) {
	resources := []ProjectResourceData{{ResourceType: "local_directory", ResourceRef: json.RawMessage(`{"local_path":"/source","daemon_id":"script-daemon","execution_mode":"script","init_script":"init.sh"}`)}}
	runtime := runtimeWithVersion("script-daemon", "99.0.0")
	if scriptClaimBlockReason(resources, runtime, false) == "" {
		t.Fatal("script task dispatched without script capability")
	}
	if reason := scriptClaimBlockReason(resources, runtime, true); reason != "" {
		t.Fatal(reason)
	}
	if reason := scriptClaimBlockReason(resources, runtimeWithVersion("another-daemon", "0.0.0"), false); reason != "" {
		t.Fatal(reason)
	}
}

func TestProjectResourceScriptCapabilityAndPersistence(t *testing.T) {
	const daemonID = "script-resource-daemon"
	projectID := dbfx.Project(t, "Script isolation")
	ref := map[string]any{"local_path": "/source", "daemon_id": daemonID, "execution_mode": "script", "init_script": "init-workspace.sh"}
	request := func() *http.Request {
		return withURLParam(newRequest("POST", "/api/projects/"+projectID+"/resources", map[string]any{"resource_type": "local_directory", "resource_ref": ref}), "id", projectID)
	}
	// A worktree-capable daemon still cannot run scripts. The new capability is
	// independent and must be checked on every resource write.
	dbfx.Runtime(t, "Old runtime", testutil.Cols{"daemon_id": daemonID, "metadata": `{"capabilities":["local-worktree-v1"]}`})
	testutil.Call(t, testHandler.CreateProjectResource, request()).Want(http.StatusUnprocessableEntity)
	dbfx.Runtime(t, "Script runtime", testutil.Cols{"provider": "handler_test_script_runtime", "daemon_id": daemonID, "metadata": `{"capabilities":["` + protocol.DaemonCapabilityLocalScriptV1 + `"]}`, "last_seen_at": testutil.Raw("now() + interval '1 second'")})
	var created ProjectResourceResponse
	testutil.Call(t, testHandler.CreateProjectResource, request()).Want(http.StatusCreated).JSON(&created)
	t.Cleanup(func() {
		req := withURLParam(newRequest("DELETE", "/api/projects/"+projectID+"/resources/"+created.ID, nil), "id", projectID)
		req = testutil.WithURLParams(req, "id", projectID, "resourceId", created.ID)
		testutil.Call(t, testHandler.DeleteProjectResource, req).Want(http.StatusNoContent)
	})
	var saved localDirectoryRef
	if err := json.Unmarshal(created.ResourceRef, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.ExecutionMode != "script" || saved.InitScript != "init-workspace.sh" {
		t.Fatalf("saved ref: %+v", saved)
	}
	ref["init_script"] = "scripts/another.sh"
	update := withURLParam(newRequest("PUT", "/api/projects/"+projectID+"/resources/"+created.ID, map[string]any{"resource_ref": ref}), "id", projectID)
	update = testutil.WithURLParams(update, "id", projectID, "resourceId", created.ID)
	var updated ProjectResourceResponse
	testutil.Call(t, testHandler.UpdateProjectResource, update).Want(http.StatusOK).JSON(&updated)
	if err := json.Unmarshal(updated.ResourceRef, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.InitScript != "scripts/another.sh" {
		t.Fatalf("script edit lost: %+v", saved)
	}
}
