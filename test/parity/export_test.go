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
		// Every repository having no export block is a corpus that measures
		// nothing while reporting success, which is the one outcome an
		// acceptance test must not produce.
		t.Fatal("no repository in the corpus has an export block; nothing was compared")
	}
}
