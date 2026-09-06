package breaking

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thegorangers/stele/internal/config"
)

// FuzzPrune is a property fuzz target, not just a crash check.
//
// Prune deletes permissions from a manifest by line-range surgery over the
// raw bytes, on the strength of yaml.Node.Line accounting worked out by
// hand (see prune.go). The invariant that surgery has to hold: every
// permission NOT targeted for removal survives, with the same (rule,
// subject, change, reason), and the file still parses afterwards. A crash
// is a finding; silently dropping a live permission while reporting
// success is worse, because nothing about the run says so — the exact
// shape of the flow-style defect prune_test.go's
// TestPruneRefusesFlowStyleList now guards by hand, found by a human
// before this fuzz target existed.
//
// manifest is the raw file. index picks which of its current
// breaking.allow entries to prune, taken modulo the entry count so any
// byte from the fuzzer is usable. Seeds are the manifests
// prune_test.go already exercises by hand — one ordinary multi-entry
// list, and the flow-style list Prune must refuse — covering both the
// property-preserving path and the refusal path.
func FuzzPrune(f *testing.F) {
	f.Add([]byte("version: 1\n"+
		"modules:\n"+
		"  - path: api\n"+
		"breaking:\n"+
		"  allow:\n"+
		"    - rule: break/field_removed\n"+
		"      subject: example.v1.Order.status\n"+
		"      reason: dropped in the v2 rollout\n"+
		"    - rule: break/field_type_changed\n"+
		"      subject: example.v1.Order.total\n"+
		"      change: int32 -> int64\n"+
		"      reason: widening; no consumer stores this in a 32-bit field\n"+
		"    - rule: break/message_removed\n"+
		"      subject: example.v1.Draft\n"+
		"      reason: dead type, never shipped to a consumer\n"), 0)

	f.Add([]byte("version: 1\n"+
		"modules:\n"+
		"  - path: api\n"+
		"breaking:\n"+
		"  allow: [{rule: break/field_removed, subject: example.v1.Order.status, "+
		"reason: dropped in the v2 rollout}, {rule: break/field_type_changed, "+
		"subject: example.v1.Order.total, change: int32 -> int64, reason: widening}]\n"), 0)

	f.Add([]byte("version: 1\n"+
		"breaking:\n"+
		"  allow:\n"+
		"    # about the removed field\n"+
		"    - rule: break/field_removed\n"+
		"      subject: example.v1.Order.status\n"+
		"      reason: dropped in the v2 rollout\n"), 0)

	f.Fuzz(func(t *testing.T, manifest []byte, index int) {
		dir := t.TempDir()
		path := filepath.Join(dir, "stele.yaml")
		if err := os.WriteFile(path, manifest, 0o644); err != nil {
			t.Fatal(err)
		}

		before, err := config.Load(path)
		if err != nil || before.Breaking == nil || len(before.Breaking.Allow) == 0 {
			// Not a manifest with a breaking.allow list to prune. Prune
			// itself still must not panic or hang on it.
			_ = Prune(path, []config.Permission{{Rule: "break/field_removed", Subject: "x"}})
			return
		}

		allow := before.Breaking.Allow
		i := index % len(allow)
		if i < 0 {
			i += len(allow)
		}
		target := allow[i]

		// Prune matches by (rule, subject, change) — deliberately not
		// reason, which is prose a reviewer may reword without the
		// permission itself changing identity (see prune.go's identity /
		// permKey). So the untouched set is every permission whose
		// (rule, subject, change) triple differs from target's: an entry
		// that shares target's triple but spells its reason differently is
		// expected to be pruned right along with target, not preserved.
		sameIdentity := func(a, b config.Permission) bool {
			return a.Rule == b.Rule && a.Subject == b.Subject && a.Change == b.Change
		}
		var untouched []config.Permission
		for _, p := range allow {
			if !sameIdentity(p, target) {
				untouched = append(untouched, p)
			}
		}

		err = Prune(path, []config.Permission{target})
		if err != nil {
			// A refusal (e.g. flow-style) must leave the file untouched.
			after, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if string(after) != string(manifest) {
				t.Fatalf("Prune returned an error but changed the file:\nerr: %v\nbefore:\n%s\nafter:\n%s",
					err, manifest, after)
			}
			return
		}

		// Success: the file must still parse, and every untouched
		// permission must still be there.
		after, err := config.Load(path)
		if err != nil {
			t.Fatalf("Prune reported success but the result does not parse: %v\nfile:\n%s", err, mustRead(t, path))
		}
		var survivors []config.Permission
		if after.Breaking != nil {
			survivors = after.Breaking.Allow
		}
		for _, want := range untouched {
			if !containsPermission(survivors, want) {
				t.Fatalf("Prune silently dropped a permission that was not targeted: %+v\nbefore:\n%s\nafter:\n%s",
					want, manifest, mustRead(t, path))
			}
		}
	})
}

func containsPermission(ps []config.Permission, want config.Permission) bool {
	for _, p := range ps {
		if p == want {
			return true
		}
	}
	return false
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
