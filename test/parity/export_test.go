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
	compared := 0
	for _, r := range corpus.Repos {
		if r.Export == nil {
			continue
		}
		compared++
		t.Run(r.Dir, func(t *testing.T) {
			repo := filepath.Join(corpus.Root, r.Dir)
			got := t.TempDir()
			runExport(t, corpus, repo, *r.Export, got)
			assertTreesEqual(t, filepath.Join(repo, r.Vendored), got)
		})
	}
	if compared == 0 {
		// Not a pass. A corpus that declares no export block is measuring
		// generation only, which is what the corpus shipped with the tool
		// does — export parity needs committed vendored trees of real
		// repositories, and that is milestone 4. A skip says so where a
		// reader will see it; reporting success would not.
		t.Skip("no repository in this corpus has an export block; export parity is not measured here")
	}
}
