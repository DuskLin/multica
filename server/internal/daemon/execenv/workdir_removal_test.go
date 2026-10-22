package execenv

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedWorkdirRemovalPreservesRemoteBranchRecord(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "workspace", "task")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	source := newTestRepo(t)
	remote := filepath.Join(t.TempDir(), "remote")
	gitRun(t, source, "clone", "--bare", source, remote)
	gitRun(t, source, "clone", remote, filepath.Join(directory, "workdir"))
	writeFile(t, filepath.Join(directory, ".DS_Store"), "Finder metadata")
	writeFile(t, filepath.Join(directory, codexHomeDirName, codexModelsCacheBindingFile), "cache fingerprint")
	if err := writeEnvRootOwner(directory, "workspace", "task"); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := RemoveManagedWorkDirectory(context.Background(), root, directory, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("directory not removed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, workdirRecoveryDir, scriptSourceName([]byte(canonical))))
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Repositories []localScriptRepository `json:"repositories"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if len(record.Repositories) != 1 || record.Repositories[0].Branch != "main" {
		t.Fatalf("missing recovery branch: %+v", record)
	}
}

func TestManagedWorkdirRemovalHonorsExecutionLock(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "workspace", "task")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeEnvRootOwner(directory, "workspace", "task"); err != nil {
		t.Fatal(err)
	}
	lock, err := openLockFile(filepath.Join(directory, envRootLockFile))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if ok, err := lockFileExclusiveNonBlocking(lock); err != nil || !ok {
		t.Fatalf("claim lock: %v", err)
	}
	if err := RemoveManagedWorkDirectory(context.Background(), root, directory, func() error { return nil }); err == nil {
		t.Fatal("deleted locked directory")
	}
}

func TestLocalScriptManualRemovalSupportsChats(t *testing.T) {
	params := scriptRecoveryFixture(t)
	params.IssueID = ""
	params.ChatSessionID = "chat-a"
	path, err := prepareLocalScript(params, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := RegisterLocalScriptSource(root, params); err != nil {
		t.Fatal(err)
	}
	sources, err := ListLocalScriptSources(root)
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources: %+v %v", sources, err)
	}
	all, err := ListLocalScriptWorkspaces(sources[0])
	if err != nil || len(all) != 1 {
		t.Fatalf("chat missing: %+v %v", all, err)
	}
	issues, err := ListLocalScriptIssues(sources[0])
	if err != nil || len(issues) != 0 {
		t.Fatalf("chat opted into automatic GC: %+v %v", issues, err)
	}
	if removed, err := CollectLocalScriptWorkspace(context.Background(), params, completedScriptIssue); err != nil || !removed {
		t.Fatalf("manual chat removal: %v %v", removed, err)
	}
	restored, err := prepareLocalScript(params, t.TempDir())
	if err != nil || restored != path {
		t.Fatalf("chat recovery: %s %v", restored, err)
	}
}
