package execenv

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// WorkdirProgress reports actual stages and measured file removals, not an
// estimated percentage. Git-managed removals report their repository instead.
type WorkdirProgress struct {
	Stage        string
	Path         string
	RemovedFiles int64
}

type workdirProgressKey struct{}
type workdirProgressReporter func(WorkdirProgress)

// WithWorkdirProgress attaches an operation-local observer to cleanup work.
func WithWorkdirProgress(ctx context.Context, report func(WorkdirProgress)) context.Context {
	return context.WithValue(ctx, workdirProgressKey{}, workdirProgressReporter(report))
}

// ReportWorkdirProgress is a no-op for scheduled cleanup without an observer.
func ReportWorkdirProgress(ctx context.Context, stage, path string, removedFiles int64) {
	if ctx == nil {
		return
	}
	if report, ok := ctx.Value(workdirProgressKey{}).(workdirProgressReporter); ok {
		report(WorkdirProgress{Stage: stage, Path: path, RemovedFiles: removedFiles})
	}
}

const removalReadBatchSize = 128

// RemoveWorkdirWithProgress removes an already authorized tree using rooted
// handles. It never follows links outside the tree and counts files only after
// successful removal. No extra pre-count scan is needed.
func RemoveWorkdirWithProgress(ctx context.Context, path string) error {
	if ctx == nil {
		return os.RemoveAll(path)
	}
	if _, ok := ctx.Value(workdirProgressKey{}).(workdirProgressReporter); !ok {
		return os.RemoveAll(path)
	}
	ReportWorkdirProgress(ctx, "deleting", path, 0)
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer parent.Close()
	return removeWorkdirEntry(ctx, parent, filepath.Base(path))
}

// removeWorkdirEntry keeps traversal anchored while checking replacement races.
func removeWorkdirEntry(ctx context.Context, parent *os.Root, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := parent.Lstat(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		if err := parent.Remove(name); err != nil {
			return err
		}
		ReportWorkdirProgress(ctx, "", "", 1)
		return nil
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return err
	}
	defer child.Close()
	current, err := child.Stat(".")
	if err != nil || !os.SameFile(info, current) {
		return errors.New("directory changed during removal")
	}
	stream, err := child.Open(".")
	if err != nil {
		return err
	}
	defer stream.Close()
	for {
		entries, readErr := stream.ReadDir(removalReadBatchSize)
		for _, entry := range entries {
			if err := removeWorkdirEntry(ctx, child, entry.Name()); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	stream.Close()
	child.Close()
	current, err = parent.Lstat(name)
	if err != nil || !os.SameFile(info, current) {
		return errors.New("directory changed before final removal")
	}
	return parent.Remove(name)
}
