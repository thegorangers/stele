package breaking

import "github.com/thegorangers/stele/internal/gitrepo"

// Unchanged reports whether every watched path has the same object in both
// revisions. Absence is compared as well as identity: a path one revision did
// not have has not "stayed the same".
func Unchanged(r *gitrepo.Repo, prev Previous, paths []string) (bool, error) {
	for _, p := range paths {
		a, aok, err := r.ObjectSHA(prev.Working, p)
		if err != nil {
			return false, err
		}
		b, bok, err := r.ObjectSHA(prev.SHA, p)
		if err != nil {
			return false, err
		}
		if aok != bok || a != b {
			return false, nil
		}
	}
	return true, nil
}
