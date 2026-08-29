package gitrepo

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrNoRemote        = errors.New("gitrepo: no remote configured")
	ErrAmbiguousRemote = errors.New("gitrepo: more than one remote configured")
)

// Remote returns the single remote configured for the repository, or an
// error naming the ambiguity when there is none or more than one: silence
// here would leave the caller guessing which remote to fetch from.
func (r *Repo) Remote() (string, error) {
	out, err := r.git("remote")
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", ErrNoRemote
	}
	remotes := strings.Fields(out)
	if len(remotes) > 1 {
		return "", fmt.Errorf("%w: %s", ErrAmbiguousRemote, strings.Join(remotes, ", "))
	}
	return remotes[0], nil
}

// BaseRef resolves branch to a remote-tracking commit, fetching it into
// refs/stele/base when it is absent. A stale local branch is never used:
// that would silently place the comparison in the past. In a GitLab
// merge-request pipeline the clone fetches only the merge-request ref, so
// the base branch is absent at any depth, and it must be fetched.
func (r *Repo) BaseRef(branch string) (string, error) {
	remote, err := r.Remote()
	if err != nil {
		return "", err
	}

	if sha, err := r.ResolveRef("refs/remotes/" + remote + "/" + branch); err == nil {
		return sha, nil
	}
	if sha, err := r.ResolveRef("refs/stele/base"); err == nil {
		return sha, nil
	}

	// The fetch writes one ref of our own and nothing else. The user's
	// remote-tracking refs and FETCH_HEAD are theirs, and this tool
	// otherwise writes only generated code and the lock.
	if _, err := r.git("fetch", "--quiet", "--no-write-fetch-head",
		remote, "+refs/heads/"+branch+":refs/stele/base"); err != nil {
		return "", err
	}
	return r.ResolveRef("refs/stele/base")
}
