//go:build windows

package execenv

// syncScriptMetadataDir leaves directory flushing to Windows; the record file
// is flushed before its atomic rename by writeScriptRecord.
func syncScriptMetadataDir(string) error { return nil }
