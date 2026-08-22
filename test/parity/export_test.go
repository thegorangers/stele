//go:build parity

package parity

import (
	"path/filepath"
	"testing"
)

// TestExport_MatchesVendoredTree measures export against the tree a real
// repository already carries: that tree is the other tool's output, committed.
//
//	go test -tags parity -run TestExport_MatchesVendoredTree ./test/parity/
func TestExport_MatchesVendoredTree(t *testing.T) {
	corpus := loadCorpus(t)
	for _, r := range corpus.Repos {
		t.Run(r.Dir, func(t *testing.T) {
			repo := filepath.Join(corpus.Root, r.Dir)
			got := t.TempDir()
			exportPerManifest(t, repo, r, got)
			assertTreesEqual(t, filepath.Join(repo, r.Vendored), got)
		})
	}
}
