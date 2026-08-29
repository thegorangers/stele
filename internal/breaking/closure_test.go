package breaking

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/thegorangers/stele/internal/gitrepo"
	"github.com/thegorangers/stele/internal/lint"
	"github.com/thegorangers/stele/internal/lockfile"
)

// closureDepTree lays out a producer repository with two files: dep.proto,
// which the consumer fixture below imports and re-exports, and unused.proto,
// which nothing imports. dep.proto's message Dep carries an extra field only
// when withExtraField is true, so two calls at different marker values model
// two revisions of the dependency that either do or do not remove a field
// the consumer re-exports.
func closureDepTree(t *testing.T, marker string, withExtraField bool) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "stele.yaml", "version: 1\nmodules:\n  - path: proto\n")

	extra := ""
	if withExtraField {
		extra = "\n  string extra = 2;"
	}
	write(t, dir, "proto/example/dep.proto",
		"syntax = \"proto3\";\npackage example;\n// "+marker+"\nmessage Dep {\n  int64 value = 1;"+extra+"\n}\n")

	write(t, dir, "proto/example/unused.proto",
		"syntax = \"proto3\";\npackage example;\n// "+marker+"\nmessage Unused { int64 x = 1; }\n")
	return dir
}

const closureDepGit = "https://example.invalid/example/closure-producer.git"

func writeClosureConsumerManifest(t *testing.T, dir string) {
	t.Helper()
	write(t, dir, lint.ManifestName, "version: 1\nmodules:\n  - path: own\n"+
		"deps:\n  - name: dep\n    git: "+closureDepGit+"\n    ref: main\n    module: proto\n")
	write(t, dir, "own/example/a.proto",
		"syntax = \"proto3\";\npackage example;\n\nimport \"example/dep.proto\";\n\n"+
			"message Owned {\n  Dep dep = 1;\n}\n")
}

func writeClosureLock(t *testing.T, dir, sha string) {
	t.Helper()
	l := &lockfile.Lock{
		Version: lockfile.Version,
		Deps:    []lockfile.Entry{{Name: "dep", Git: closureDepGit, Ref: "main", SHA: sha}},
	}
	if err := lockfile.Save(filepath.Join(dir, lint.LockName), l); err != nil {
		t.Fatal(err)
	}
}

// loadClosurePair commits the consumer with lock sha prev, then with lock sha
// cur, and loads both revisions against a fetch that serves treePrev and
// treeCur for those two shas respectively.
func loadClosurePair(t *testing.T, treePrev, treeCur string) (prev, cur Revision) {
	t.Helper()
	dir := repo(t)
	writeClosureConsumerManifest(t, dir)
	writeClosureLock(t, dir, revShaOne)
	prevSHA := commit(t, dir, "marker.txt", "prev", "resolves against shaOne")

	writeClosureLock(t, dir, revShaTwo)
	curSHA := commit(t, dir, "marker.txt", "cur", "resolves against shaTwo")

	m := &movingFetch{trees: map[string]string{revShaOne: treePrev, revShaTwo: treeCur}}
	r, err := gitrepo.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	prev, err = Load(context.Background(), r, prevSHA, m.fetch, true)
	if err != nil {
		t.Fatalf("Load(prev): %v", err)
	}
	cur, err = Load(context.Background(), r, curSHA, m.fetch, false)
	if err != nil {
		t.Fatalf("Load(cur): %v", err)
	}
	return prev, cur
}

// A repository whose own protos are byte-identical between two revisions,
// whose lock moves a dependency to a revision where a message it re-exports
// lost a field: ClassifyClosure reports exactly one finding, on the
// dependency's declaration, marked as a closure finding.
func TestClassifyClosureReportsRemovedFieldInReexportedDependency(t *testing.T) {
	treePrev := closureDepTree(t, "v1", true)
	treeCur := closureDepTree(t, "v2", false)

	prev, cur := loadClosurePair(t, treePrev, treeCur)

	findings := ClassifyClosure(prev, cur)
	if len(findings) != 1 {
		t.Fatalf("ClassifyClosure findings = %d, want exactly 1: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Rule != "break/field_removed" {
		t.Errorf("Rule = %q, want break/field_removed", f.Rule)
	}
	if f.Subject != "example.Dep.extra" {
		t.Errorf("Subject = %q, want example.Dep.extra", f.Subject)
	}
	if !f.Closure {
		t.Errorf("Closure = false, want true: this finding is in the resolved closure, not in owned files")
	}
	if want := "dep"; !contains(f.Message, want) {
		t.Errorf("Message = %q, want it to name the dependency %q", f.Message, want)
	}
}

// A dependency bump that changes nothing reachable reports nothing: the
// consumer re-exports the same shape either side of the bump.
func TestClassifyClosureNoFindingsWhenReachableShapeUnchanged(t *testing.T) {
	treePrev := closureDepTree(t, "v1", true)
	treeCur := closureDepTree(t, "v2", true) // same fields, different comment marker

	prev, cur := loadClosurePair(t, treePrev, treeCur)

	findings := ClassifyClosure(prev, cur)
	if len(findings) != 0 {
		t.Fatalf("ClassifyClosure findings = %+v, want none: nothing reachable changed shape", findings)
	}
}

// A change in a dependency file that is NOT reachable from the owned
// modules reports nothing. unused.proto changes field count between the two
// trees; nothing in the consumer imports it, so it must never surface here —
// this is the test that stops the closure comparison becoming "diff the
// whole world".
func TestClassifyClosureIgnoresUnreachableDependencyFile(t *testing.T) {
	dir := repo(t)
	writeClosureConsumerManifest(t, dir)
	writeClosureLock(t, dir, revShaOne)
	prevSHA := commit(t, dir, "marker.txt", "prev", "resolves against shaOne")
	writeClosureLock(t, dir, revShaTwo)
	curSHA := commit(t, dir, "marker.txt", "cur", "resolves against shaTwo")

	treePrev := t.TempDir()
	write(t, treePrev, "stele.yaml", "version: 1\nmodules:\n  - path: proto\n")
	write(t, treePrev, "proto/example/dep.proto",
		"syntax = \"proto3\";\npackage example;\nmessage Dep {\n  int64 value = 1;\n}\n")
	write(t, treePrev, "proto/example/unused.proto",
		"syntax = \"proto3\";\npackage example;\nmessage Unused {\n  int64 x = 1;\n}\n")

	treeCur := t.TempDir()
	write(t, treeCur, "stele.yaml", "version: 1\nmodules:\n  - path: proto\n")
	write(t, treeCur, "proto/example/dep.proto",
		"syntax = \"proto3\";\npackage example;\nmessage Dep {\n  int64 value = 1;\n}\n")
	// unused.proto loses a field between prev and cur: nothing reachable
	// imports it, so this must not be reported.
	write(t, treeCur, "proto/example/unused.proto",
		"syntax = \"proto3\";\npackage example;\nmessage Unused {\n  int64 x = 1;\n  string y = 2;\n}\n")

	m := &movingFetch{trees: map[string]string{revShaOne: treePrev, revShaTwo: treeCur}}
	r, err := gitrepo.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	prev, err := Load(context.Background(), r, prevSHA, m.fetch, true)
	if err != nil {
		t.Fatalf("Load(prev): %v", err)
	}
	cur, err := Load(context.Background(), r, curSHA, m.fetch, false)
	if err != nil {
		t.Fatalf("Load(cur): %v", err)
	}

	findings := ClassifyClosure(prev, cur)
	if len(findings) != 0 {
		t.Fatalf("ClassifyClosure findings = %+v, want none: the changed file is not reachable from owned imports", findings)
	}

	reach := Reachable(cur)
	for _, p := range reach {
		if p == "example/unused.proto" {
			t.Fatalf("Reachable(cur) = %v, must not include example/unused.proto: nothing owned imports it", reach)
		}
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
