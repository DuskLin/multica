package execenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceCleanupMetadataBoundaries(t *testing.T) {
	checks := []struct {
		name    string
		check   func(string, []string) error
		managed bool
	}{
		{name: "managed", check: managedFilesCovered, managed: true},
		{name: "script", check: scriptFilesCoveredByRepositories},
	}
	tests := []struct {
		name        string
		path        string
		symlink     bool
		allow       bool
		managedOnly bool
	}{
		{name: "Finder metadata", path: ".DS_Store", allow: true},
		{name: "nested Finder metadata", path: "container/.DS_Store", allow: true},
		{name: "cache binding", path: "codex-home/.models_cache_config.sha256", allow: true, managedOnly: true},
		{name: "binding at root", path: ".models_cache_config.sha256"},
		{name: "binding in unrelated directory", path: "other/.models_cache_config.sha256"},
		{name: "binding in nested home", path: "other/codex-home/.models_cache_config.sha256"},
		{name: "user output", path: "result.txt"},
		{name: "Codex session", path: "codex-home/sessions/session.jsonl"},
		{name: "Finder named directory", path: ".DS_Store/result.txt"},
		{name: "binding named directory", path: "codex-home/.models_cache_config.sha256/result.txt"},
		{name: "Finder symlink", path: ".DS_Store", symlink: true},
		{name: "binding symlink", path: "codex-home/.models_cache_config.sha256", symlink: true},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					directory := t.TempDir()
					path := filepath.Join(directory, filepath.FromSlash(tc.path))
					if tc.symlink {
						target := filepath.Join(t.TempDir(), "result.txt")
						writeFile(t, target, "user data")
						if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(target, path); err != nil {
							t.Fatal(err)
						}
					} else {
						writeFile(t, path, "test data")
					}
					err := check.check(directory, nil)
					wantAllowed := tc.allow && (!tc.managedOnly || check.managed)
					if (err == nil) != wantAllowed {
						t.Fatalf("check = %v, want allowed = %v", err, wantAllowed)
					}
				})
			}
		})
	}
}
