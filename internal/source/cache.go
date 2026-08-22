package source

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// CacheDir returns the directory a repository at url, at commit sha, is laid
// out in under cacheRoot.
//
// The layout is <cacheRoot>/<host>/<repo path>/<sha>. The repository path is
// kept segment by segment rather than flattened, and it may have any number of
// segments: on GitLab a project lives under a chain of subgroups, so nothing
// here may assume exactly two components.
//
// The transport is deliberately not part of the key: the same repository
// fetched over SSH from a workstation and over HTTPS from CI is the same
// content, and must not occupy two entries. The sha makes the entry immutable,
// which is what allows a present entry to be reused without any network access.
func CacheDir(cacheRoot, url, sha string) string {
	return filepath.Join(cacheRoot, filepath.Join(cacheKey(url)...), sha)
}

// cacheKey turns a clone URL into the path segments identifying the repository.
//
// A well-formed address is decomposed into host and repository path. Anything
// else — most importantly a local filesystem path, which git clones happily and
// tests rely on — has no host to speak of, so it is identified by a digest of
// the URL. A digest is used rather than the path itself because a filesystem
// path is not portable into a cache layout: it may be absolute, may contain
// characters this cache would have to escape, and may be arbitrarily deep.
func cacheKey(url string) []string {
	if addr, err := ParseAddr(url, nil); err == nil {
		segments := []string{addr.Host}
		segments = append(segments, strings.Split(addr.Path, "/")...)
		return segments
	}
	sum := sha256.Sum256([]byte(url))
	return []string{"local", hex.EncodeToString(sum[:])[:32]}
}
