// Package atomicfile replaces a file's contents in one step, or leaves the
// file alone.
//
// It is one function, and it is a package because two of the files this tool
// generates need it and a second copy of it would be a second place to get it
// subtly wrong. The reasoning that made it necessary is in Write.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write replaces path with content in one step, or leaves it alone.
//
// A plain write opens the existing file for truncation first, so a process
// killed mid-write, or a disk that fills, destroys the record it was replacing
// and leaves half a file in its place. The dangerous half is not the one that
// fails to parse: YAML truncated on an entry boundary loads cleanly as a
// shorter list than the run that wrote it produced — a lock pinning fewer
// dependencies, or a baseline holding fewer findings, either of which is a
// silently weaker file. The content is written beside its destination and
// renamed, so a reader — or a git status — sees the old file or the new one
// and never a prefix.
func Write(path string, content []byte) error {
	dir := filepath.Dir(path)
	// The temporary file is a sibling so the rename stays on one filesystem,
	// which is what makes it atomic. The dot keeps it out of the way of
	// anything listing the directory in the moment it exists.
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	tmp := f.Name()
	// Removing it covers every exit: an error before the rename, and the
	// successful path where the rename has already taken the name away.
	defer os.Remove(tmp)

	if _, err := f.Write(content); err != nil {
		f.Close()
		return fmt.Errorf("%s: %w", path, err)
	}
	// These files are committed and read by people, so they are 0644 like
	// every other file in the tree; CreateTemp makes it 0600.
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
