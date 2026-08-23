//go:build parity

package parity

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestExport_MatchesReference measures export, the half of this tool that
// removes the registry.
//
// There are two ways a corpus can say what the expected bytes are, and both
// are measured here because they answer different questions.
//
//   - A checkout that carries a vendored tree is compared against it. That
//     tree is the other tool's output, committed by the builds that actually
//     ran, so it measures against months of the reference tool's behaviour
//     rather than against one run of it. Only the union of every invocation
//     can be compared, because the union is what produced the tree.
//
//   - A checkout that carries none — the synthetic corpus shipped here cannot,
//     since nothing produced one — has the other tool run over the same
//     physical files, one invocation at a time. That is the more diagnosable
//     of the two: a failure names the invocation.
//
//     go test -tags parity -run TestExport_MatchesReference ./test/parity/
func TestExport_MatchesReference(t *testing.T) {
	corpus := loadCorpus(t)
	compared := 0
	for _, r := range corpus.Repos {
		if r.Export == nil {
			continue
		}
		repo := filepath.Join(corpus.Root, r.Dir)
		if r.Vendored != "" {
			compared++
			t.Run(r.Dir, func(t *testing.T) {
				got := t.TempDir()
				runExport(t, corpus, repo, *r.Export, got)
				assertTreesEqual(t, filepath.Join(repo, r.Vendored), got)
			})
			continue
		}
		stage := stageManifest(t, corpus, repo, r.Export.Manifest)
		for i, inv := range r.Export.Invocations {
			compared++
			t.Run(fmt.Sprintf("%s/%s", r.Dir, inv.name(i)), func(t *testing.T) {
				want := t.TempDir()
				runReferenceExport(t, corpus, repo, inv, want)
				// An empty expectation would make the comparison below pass
				// while measuring nothing.
				if n := len(listFiles(t, want)); n == 0 {
					t.Fatalf("the reference tool exported no files from %s", repo)
				}
				got := t.TempDir()
				runOneExport(t, corpus, stage, repo, inv, got)
				assertTreesEqual(t, want, got)
				assertAbsent(t, "the reference tool", want, inv.ExpectAbsent)
				assertAbsent(t, "stele", got, inv.ExpectAbsent)
			})
		}
	}
	if compared == 0 {
		// Not a skip. Milestone 3 shipped this as one, because no corpus
		// declared an export block and saying so was more honest than
		// reporting a pass. Now that the shipped corpus declares them, a skip
		// would mean a corpus had lost its export blocks and nobody was told:
		// a test that cannot fail, which is the failure mode this project has
		// been caught by before.
		t.Fatal("no repository in the corpus has an export block; nothing was compared")
	}
}

// name labels an invocation in the test output, so that a failure says which
// one failed rather than which index it had.
func (inv Invocation) name(i int) string {
	parts := []string{fmt.Sprintf("%d", i)}
	if inv.Dep != "" {
		parts = append(parts, "dep="+inv.Dep)
	} else {
		parts = append(parts, "own-modules")
	}
	if len(inv.Paths) > 0 {
		parts = append(parts, "path="+strings.Join(inv.Paths, ","))
	}
	if inv.ExcludeImports {
		parts = append(parts, "exclude-imports")
	}
	return strings.ReplaceAll(strings.Join(parts, "_"), "/", ".")
}

// runReferenceExport exports with the tool being replaced, in the checkout
// itself, writing to out.
//
// The checkout is used as it stands rather than staged, as in runReference:
// its configuration is the reference, and copying it would introduce a second
// thing that could be wrong. Nothing is written into it.
func runReferenceExport(t *testing.T, c Corpus, repo string, inv Invocation, out string) {
	t.Helper()
	bin := referenceBin(t, c)
	args := []string{"export", inv.Reference.Input, "-o", out}
	for _, p := range inv.Reference.Paths {
		args = append(args, "--path", p)
	}
	if inv.ExcludeImports {
		args = append(args, "--exclude-imports")
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = repo
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v in %s: %v\n%s", bin, args, repo, err, b)
	}
}

// assertAbsent fails if a tree holds any file under one of the given prefixes.
//
// Comparing two trees proves they agree; it does not prove they are right. The
// well-known types are the case where that distinction matters: neither tool
// emits them, and if both started to, a comparison of the two would go on
// passing. So the decision is stated here as well as measured.
func assertAbsent(t *testing.T, who, dir string, prefixes []string) {
	t.Helper()
	if len(prefixes) == 0 {
		return
	}
	var found []string
	for p := range listFiles(t, dir) {
		for _, prefix := range prefixes {
			if p == prefix || strings.HasPrefix(p, strings.TrimSuffix(prefix, "/")+"/") {
				found = append(found, p)
			}
		}
	}
	if len(found) > 0 {
		t.Errorf("%s exported %d file(s) the corpus says must not be exported: %s\n"+
			"these paths are supplied by no repository — every compiler carries them — so exporting them would vendor a copy of what the compiler already has",
			who, len(found), strings.Join(found, ", "))
	}
}
