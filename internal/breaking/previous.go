// Package breaking chooses the revision a working revision is compared
// against for breaking-change detection.
package breaking

import "github.com/thegorangers/stele/internal/gitrepo"

// Previous names the revision Choose settled on, the working revision it was
// compared from, and why: SHA is what to diff against, Working is the
// revision that supplied it (HEAD itself, or the topic side of a merge
// commit), and Reason explains the choice for diagnostics.
type Previous struct{ SHA, Working, Reason string }

// Choose returns the revision this one is compared against.
//
// The order of the three questions is the whole of this function, and getting
// it wrong is not theoretical: asking "is this a merge with one parent on the
// base" before "is HEAD on the base" sends every ordinary merge on the base
// branch into the nothing-to-compare exit, because on the base branch every
// parent of HEAD is an ancestor of the base. That is the degenerate
// base-branch comparison this design exists to avoid, reached by a different
// route. So: on the base branch first, fabricated merge second.
func Choose(r *gitrepo.Repo, base string) (Previous, bool, error) {
	head, err := r.Head()
	if err != nil {
		return Previous{}, false, err
	}
	parents, err := r.Parents(head)
	if err != nil {
		return Previous{}, false, err
	}
	if len(parents) == 0 {
		return Previous{}, false, nil
	}
	baseSHA, err := r.BaseRef(base)
	if err != nil {
		return Previous{}, false, err
	}

	// Is HEAD itself on the base branch? Then the previous revision is what
	// the branch looked like before this commit: its first parent.
	onBase, err := r.IsAncestor(head, baseSHA)
	if err != nil {
		return Previous{}, false, err
	}
	if onBase {
		return Previous{SHA: parents[0], Working: head,
			Reason: "the first parent, because HEAD is on " + base}, true, nil
	}

	// HEAD is off the base. A two-parent HEAD with exactly one parent on the
	// base is a merge of the base into a topic branch, which is also the
	// shape a pull-request checkout fabricates. Ancestry, not equality with
	// the tip: the tip moves between checkout and run.
	working := head
	if len(parents) == 2 {
		var on [2]bool
		for i, p := range parents {
			if on[i], err = r.IsAncestor(p, baseSHA); err != nil {
				return Previous{}, false, err
			}
		}
		if on[0] != on[1] {
			working = parents[0]
			if on[0] {
				working = parents[1]
			}
		}
	}
	prev, err := r.MergeBase(working, baseSHA)
	if err != nil {
		return Previous{}, false, err
	}
	reason := "merge-base with " + base
	if working != head {
		reason += ", from the topic side of a merge commit"
	}
	return Previous{SHA: prev, Working: working, Reason: reason}, true, nil
}
