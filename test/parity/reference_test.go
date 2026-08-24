//go:build parity

package parity

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/plugin"
)

// TestReferencePinIsWholeAndInOneFile asserts that the corpus alone says which
// build of the reference tool parity was measured against — the version and
// the bytes.
//
// The pin used to live in two files: the version in corpus.yaml and the
// sha256 of the download in the CI workflow. It failed closed, but a pin split
// across two files is a pin two people can half-update, and only one of the
// two halves is visible to somebody running the harness on a workstation.
func TestReferencePinIsWholeAndInOneFile(t *testing.T) {
	c := loadCorpus(t)
	if c.BufVersion == "" {
		t.Fatal("the corpus pins no reference tool version")
	}
	if len(c.BufDownloads) == 0 {
		t.Fatal("the corpus pins a version but no bytes; the digest of the download belongs beside the version, not in a CI workflow")
	}
	d, err := plugin.Select("the reference tool", config.PluginDownloads(c.BufDownloads), runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	if d.SHA256 == "" {
		t.Fatalf("the corpus declares a download for %s with no sha256", d.Platform())
	}
	if os.Getenv(EnvCorpus) != "" {
		return
	}
	// The shipped corpus is checked further: its downloads must be the tool's
	// own release binaries at the pinned version, so that a bumped version
	// with a stale url cannot pass.
	for _, e := range c.BufDownloads {
		if want := "/v" + c.BufVersion + "/"; !strings.Contains(e.URL, want) {
			t.Errorf("download for %s/%s is %s, which does not name the pinned version %s",
				e.OS, e.Arch, e.URL, c.BufVersion)
		}
	}
}
