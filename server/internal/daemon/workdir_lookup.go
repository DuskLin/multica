package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// findWorkDirectory enumerates only owned root names and script registrations.
// It never measures unrelated trees, fetches task titles, or trusts a UI path.
func (d *Daemon) findWorkDirectory(ctx context.Context, id string) (WorkDirectory, error) {
	root := d.cfg.WorkspacesRoot
	workspaces, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return WorkDirectory{}, err
	}
	for _, workspace := range workspaces {
		if err := ctx.Err(); err != nil {
			return WorkDirectory{}, err
		}
		if !workspace.IsDir() || strings.HasPrefix(workspace.Name(), ".") {
			continue
		}
		parent := filepath.Join(root, workspace.Name())
		directories, err := os.ReadDir(parent)
		if err != nil {
			continue
		}
		for _, directory := range directories {
			path := filepath.Join(parent, directory.Name())
			if !directory.IsDir() || workdirID(path) != id {
				continue
			}
			owner, err := d.gcTaskDirOwner(path)
			if err != nil {
				return WorkDirectory{}, err
			}
			return WorkDirectory{ID: id, Profile: d.cfg.Profile, Path: path, Mode: "managed", WorkspaceID: owner.WorkspaceID}, nil
		}
	}
	sources, err := execenv.ListLocalScriptSources(root)
	if err != nil {
		return WorkDirectory{}, err
	}
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return WorkDirectory{}, err
		}
		directories, err := execenv.ListLocalScriptWorkspaces(source)
		if err != nil {
			return WorkDirectory{}, err
		}
		for _, params := range directories {
			path, err := params.WorkspacePath()
			if err != nil || workdirID(path) != id {
				continue
			}
			kind, parent := "issue", params.IssueID
			if parent == "" {
				kind, parent = "chat", params.ChatSessionID
			}
			if parent == "" {
				kind, parent = "run", params.TaskID
			}
			info, err := os.Lstat(path)
			if err != nil || !info.IsDir() {
				return WorkDirectory{}, errors.New("managed directory is missing or replaced")
			}
			return WorkDirectory{ID: id, Profile: d.cfg.Profile, Path: path, Mode: "script", WorkspaceID: params.WorkspaceID, Kind: kind, ParentID: parent}, nil
		}
	}
	return WorkDirectory{}, fmt.Errorf("directory is no longer present or is not managed: %s", id)
}
