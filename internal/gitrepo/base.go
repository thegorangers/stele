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

// BaseRef resolves branch to the commit it points at on the remote, fetching
// on every call. A cached answer would silently place the comparison in the
// past: the base moves, and a clone that fetched once and never again would
// keep comparing against where the base used to be. So the fetch runs first,
// unconditionally, into refs/stele/base — the same ref, on every call, so a
// second fetch simply moves it.
//
// fresh reports whether that fetch succeeded. When it did not — no network,
// an offline developer — BaseRef falls back to whatever was fetched before:
// first refs/stele/base from an earlier call, then, for a clone that has
// never called BaseRef but does have an ordinary remote-tracking branch,
// refs/remotes/<remote>/<branch>. fresh is false whenever a fallback was
// used, so a caller can say so rather than comparing against a stale base in
// silence. When the fetch fails and neither fallback exists, BaseRef returns
// the fetch error.
func (r *Repo) BaseRef(branch string) (sha string, fresh bool, err error) {
	remote, err := r.Remote()
	if err != nil {
		return "", false, err
	}

	// The fetch writes one ref of our own and nothing else. The user's
	// remote-tracking refs and FETCH_HEAD are theirs, and this tool
	// otherwise writes only generated code and the lock.
	fetchErr := func() error {
		_, err := r.git("fetch", "--quiet", "--no-write-fetch-head",
			remote, "+refs/heads/"+branch+":refs/stele/base")
		return err
	}()
	if fetchErr == nil {
		sha, err = r.ResolveRef("refs/stele/base")
		if err != nil {
			return "", false, err
		}
		return sha, true, nil
	}

	if sha, err := r.ResolveRef("refs/stele/base"); err == nil {
		return sha, false, nil
	}
	if sha, err := r.ResolveRef("refs/remotes/" + remote + "/" + branch); err == nil {
		return sha, false, nil
	}
	return "", false, fetchErr
}
