// Package hashing holds the one definition of what a file's identity is.
//
// It exists because two of them existed. Resolution hashes every proto file it
// admits, and the lock hashes every file of a pinned tree; for a proto file
// under a module root those are the same bytes, and two implementations over
// the same bytes are the second copy of a fact that the design forbids
// everywhere else. They had already drifted — one refused symbolic links, the
// other skipped them — which is exactly how such a copy fails: not loudly, but
// by quietly disagreeing about a case nobody looked at.
//
// What the two callers do with a non-regular file stays their own decision,
// because the questions differ: resolution asks which files may be imported,
// the lock asks what the tree contains. Only the hash itself is shared.
package hashing

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// File returns the hex sha256 of a file's contents. The file is streamed
// rather than read whole: nothing here needs the bytes, only their identity.
func File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Bytes returns the hex sha256 of bytes already in hand. It is here rather
// than at its one call site so that there stays exactly one answer to what a
// sha256 in this tool looks like.
func Bytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
