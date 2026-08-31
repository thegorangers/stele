package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thegorangers/stele/internal/config"
)

// FuzzParseManifest fuzzes manifest parsing end to end (Load, meaning the
// YAML decode and the validate pass together). It never expects a
// particular answer: any input either parses into a valid *File or is
// refused with an error. What it must never do is panic or fail to
// terminate — stele.yaml, stele.lock and the .proto files a dependency
// repository ships all reach this parser by way of internal/resolve,
// so a crash here is a third party's manifest taking down someone else's
// CI.
//
// Seeds come from the corpus that already exists for the manifest schema:
// schema/testdata/manifest/{valid,invalid}. Those are the shapes this
// parser actually meets, hand-written to probe the dual-shape fields and
// the strict-decode paths (decodeStrict, the stringList UnmarshalYAML
// methods) — a better seed set than anything invented for this fuzz target
// alone.
func FuzzParseManifest(f *testing.F) {
	for _, dir := range []string{
		"../../schema/testdata/manifest/valid",
		"../../schema/testdata/manifest/invalid",
	} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			f.Fatalf("reading seed corpus %s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				f.Fatalf("reading seed %s: %v", e.Name(), err)
			}
			f.Add(b)
		}
	}

	f.Fuzz(func(t *testing.T, manifest []byte) {
		dir := t.TempDir()
		path := filepath.Join(dir, "stele.yaml")
		if err := os.WriteFile(path, manifest, 0o644); err != nil {
			t.Fatal(err)
		}
		// Either return value is fine. A panic, or a hang under -fuzz's
		// timeout, is the only outcome that fails this test.
		_, _ = config.Load(path)
	})
}
