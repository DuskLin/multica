package execenv

import (
	"io/fs"
	"path/filepath"
)

const (
	codexHomeDirName       = "codex-home"
	codexSandboxBinDirName = ".sandbox-bin"
	finderMetadataFileName = ".DS_Store"
)

// isReclaimableFinderMetadata reports whether entry is a regular Finder metadata
// file. Directories and links with the same name must retain normal protection.
func isReclaimableFinderMetadata(entry fs.DirEntry) bool {
	return entry.Type().IsRegular() && entry.Name() == finderMetadataFileName
}

// ManagedReclaimableArtifactSubpaths returns daemon-owned, regenerable
// directories inside a task env root. Callers must match these as exact
// relative paths rather than basenames: a repository may legitimately contain
// a directory with the same leaf name.
func ManagedReclaimableArtifactSubpaths() []string {
	return []string{filepath.Join(codexHomeDirName, codexSandboxBinDirName)}
}
