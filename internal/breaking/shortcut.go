package breaking

import (
	"errors"

	"github.com/thegorangers/stele/internal/gitrepo"
)

// TreesUnchanged reports whether every watched path has the same object in both
// revisions. Absence is compared as well as identity: a path one revision did
// not have has not "stayed the same".
//
// paths must not be empty: an empty set would compare nothing and report
// "unchanged" forever, which is the empty-comparison-as-clean failure this
// package exists to avoid.
func TreesUnchanged(r *gitrepo.Repo, prev Previous, paths []string) (bool, error) {
	if len(paths) == 0 {
		return false, errors.New("breaking: nothing to compare against: no paths were watched")
	}
	for _, p := range paths {
		a, aok, err := r.ObjectSHA(prev.Working, p)
		if err != nil {
			return false, err
		}
		b, bok, err := r.ObjectSHA(prev.SHA, p)
		if err != nil {
			return false, err
		}
		// aok != bok is redundant given ObjectSHA's current contract
		// (empty SHA iff absent, pinned by
		// gitrepo.TestObjectSHAEmptyIffAbsent), so a == b alone would
		// already catch this. It stays here belt-and-braces: if that
		// contract ever changes, this is what stops "appeared" from
		// silently being read as "unchanged".
		if aok != bok || a != b {
			return false, nil
		}
	}
	return true, nil
}
