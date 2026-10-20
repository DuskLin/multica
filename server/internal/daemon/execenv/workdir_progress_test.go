package execenv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkdirProgressRemovalCountsFilesWithoutFollowingLinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	const fileCount = removalReadBatchSize*2 + 1
	for i := range fileCount {
		if err := os.WriteFile(filepath.Join(target, "nested", fmt.Sprintf("file-%d", i)), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(outside, "keep")
	if err := os.WriteFile(keep, []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(target, "outside")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	var removed int64
	ctx := WithWorkdirProgress(context.Background(), func(progress WorkdirProgress) { removed += progress.RemovedFiles })
	if err := RemoveWorkdirWithProgress(ctx, target); err != nil {
		t.Fatal(err)
	}
	if removed != fileCount+1 {
		t.Fatalf("count=%d, want %d", removed, fileCount+1)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target still exists: %v", err)
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != "protected" {
		t.Fatalf("symlink destination was changed: %v", err)
	}
}

func TestWorkdirProgressCancellationStopsBeforeRemovingMoreFiles(t *testing.T) {
	target := t.TempDir()
	for i := range removalReadBatchSize {
		if err := os.WriteFile(filepath.Join(target, fmt.Sprintf("file-%d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = WithWorkdirProgress(ctx, func(progress WorkdirProgress) {
		if progress.RemovedFiles > 0 {
			cancel()
		}
	})
	if err := RemoveWorkdirWithProgress(ctx, target); err != context.Canceled {
		t.Fatalf("cancellation ignored: %v", err)
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != removalReadBatchSize-1 {
		t.Fatalf("unexpected partial deletion: count=%d err=%v", len(entries), err)
	}
}
