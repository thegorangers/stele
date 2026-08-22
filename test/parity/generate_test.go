//go:build parity

package parity

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/gen"
)

// TestGenerate_ParityWithBuf is the criterion the whole tool rests on: run the
// other tool and this one over the same sources, and compare the two output
// trees byte for byte.
//
// The two are driven by different configuration on purpose — that is what
// makes the translation checkable. What is compared is output.
//
//	go test -tags parity -run TestGenerate_ParityWithBuf ./test/parity/
func TestGenerate_ParityWithBuf(t *testing.T) {
	corpus := loadCorpus(t)
	compared := 0
	for _, r := range corpus.Repos {
		if r.Generate == nil {
			continue
		}
		compared++
		t.Run(r.Dir, func(t *testing.T) {
			repo := filepath.Join(corpus.Root, r.Dir)
			want := t.TempDir()
			runReference(t, corpus, repo, *r.Generate, want)
			got := t.TempDir()
			// An empty reference tree would make the comparison below pass
			// while measuring nothing, which is the failure mode this whole
			// test exists to avoid.
			if n := len(listFiles(t, want)); n == 0 {
				t.Fatalf("the reference tool generated no files from %s", repo)
			}
			runStele(t, corpus, repo, *r.Generate, got)
			assertTreesEqual(t, want, got)
		})
	}
	if compared == 0 {
		// Every repository having no generate block is a corpus that measures
		// nothing while reporting success, which is the one outcome an
		// acceptance test must not produce.
		t.Fatal("no repository in the corpus has a generate block; nothing was compared")
	}
}

// runReference generates with the tool being replaced, in the checkout itself,
// writing to out.
//
// The checkout is used as it stands rather than staged: its configuration is
// the reference, and copying it would introduce a second thing that could be
// wrong. Nothing is written into it — the output goes to out.
func runReference(t *testing.T, c Corpus, repo string, g Generate, out string) {
	t.Helper()
	bin := c.Buf
	if bin == "" {
		bin = "buf"
	}
	args := []string{"generate", "-o", out}
	if g.IncludeImports {
		args = append(args, "--include-imports")
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = repo
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v in %s: %v\n%s", bin, args, repo, err, b)
	}
}

// runStele generates with this tool, into out.
//
// The checkout is staged rather than generated into: the manifest lives in the
// corpus, not in the checkout, and every path in it — module roots and plugin
// output directories alike — is relative to the manifest. So the staging area
// gets a copy of each module the manifest declares, the manifest beside them,
// and becomes the directory the run happens in. The checkout is left untouched,
// which matters because it is somebody's working copy and because a test that
// modifies what it measures measures the wrong thing.
func runStele(t *testing.T, c Corpus, repo string, g Generate, out string) {
	t.Helper()
	manifest := filepath.Join(c.dir, filepath.FromSlash(g.Manifest))
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(manifest)
	if err != nil {
		t.Fatalf("%s: %v", manifest, err)
	}

	stage := t.TempDir()
	for _, m := range cfg.Modules {
		copyTree(t, filepath.Join(repo, filepath.FromSlash(m.Path)), filepath.Join(stage, filepath.FromSlash(m.Path)))
	}
	if err := os.WriteFile(filepath.Join(stage, gen.ManifestName), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = gen.Run(context.Background(), gen.Options{
		Dir:            stage,
		IncludeImports: g.IncludeImports,
		// Every module of these manifests is local, so nothing is fetched; a
		// fetcher that refuses says so plainly instead of reaching the network
		// from an acceptance test.
		Fetch:  refuseFetch,
		NoLock: true,
	})
	if err != nil {
		t.Fatalf("generating %s: %v", repo, err)
	}

	// The plugins wrote under the staging area, in the directories the
	// manifest names. Those are what the other tool's -o directory holds, so
	// the whole staging area minus the sources is the tree to compare.
	for _, p := range pluginOuts(cfg) {
		copyTree(t, filepath.Join(stage, filepath.FromSlash(p)), filepath.Join(out, filepath.FromSlash(p)))
	}
}

// pluginOuts returns every distinct output directory the manifest names.
func pluginOuts(cfg *config.File) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range cfg.Generate {
		for _, p := range t.Plugins {
			if p.Out == "" || seen[p.Out] {
				continue
			}
			seen[p.Out] = true
			out = append(out, p.Out)
		}
	}
	return out
}

func refuseFetch(_ context.Context, git, ref string) (string, string, error) {
	return "", "", errNoFetch{git: git, ref: ref}
}

type errNoFetch struct{ git, ref string }

func (e errNoFetch) Error() string {
	return "the parity corpus declares only local modules, but " + e.git + "@" + e.ref + " was asked for"
}
