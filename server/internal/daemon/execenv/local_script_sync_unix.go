//go:build !windows

package execenv

import "os"

// syncScriptMetadataDir persists a published recovery record before deletion.
func syncScriptMetadataDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
