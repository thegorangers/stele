package breaking

import (
	"testing"

	"github.com/thegorangers/stele/internal/gitrepo"
)

func TestBaseBranchUsesFirstParent(t *testing.T) {
	dir := repo(t)
	commit(t, dir, "a.txt", "one", "first")
	run(t, dir, "checkout", "-q", "-b", "topic")
	commit(t, dir, "b.txt", "two", "topic work")
	run(t, dir, "checkout", "-q", "main")
	commit(t, dir, "c.txt", "three", "main moves")
	mainBefore := run(t, dir, "rev-parse", "HEAD")
	run(t, dir, "merge", "-q", "--no-ff", "-m", "merge topic", "topic")

	r, _ := gitrepo.Open(dir)
	got, ok, err := Choose(r, "main")
	if err != nil || !ok {
		t.Fatalf("Choose: ok=%v err=%v", ok, err)
	}
	if got.SHA != mainBefore {
		t.Fatalf("previous = %s, want the first parent %s (reason %q)", got.SHA, mainBefore, got.Reason)
	}
}

func TestTopicBranchUsesMergeBaseNotTip(t *testing.T) {
	dir := repo(t)
	fork := commit(t, dir, "a.txt", "one", "first")
	run(t, dir, "checkout", "-q", "-b", "topic")
	topicHead := commit(t, dir, "b.txt", "two", "topic work")
	run(t, dir, "checkout", "-q", "main")
	commit(t, dir, "neighbour.txt", "x", "a neighbour lands")
	run(t, dir, "checkout", "-q", "topic")

	r, _ := gitrepo.Open(dir)
	got, ok, _ := Choose(r, "main")
	if !ok || got.SHA != fork {
		t.Fatalf("previous = %s, want the fork point %s", got.SHA, fork)
	}
	if got.Working != topicHead {
		t.Fatalf("working = %s, want %s", got.Working, topicHead)
	}
}

func TestFabricatedMergeUsesTopicParentAndSurvivesBaseMoving(t *testing.T) {
	dir := repo(t)
	fork := commit(t, dir, "a.txt", "one", "first")
	run(t, dir, "checkout", "-q", "-b", "topic")
	topicHead := commit(t, dir, "b.txt", "two", "topic work")
	run(t, dir, "checkout", "-q", "main")
	commit(t, dir, "n.txt", "x", "neighbour")
	run(t, dir, "checkout", "-q", "--detach")
	run(t, dir, "merge", "-q", "--no-ff", "-m", "pr merge", "topic")
	run(t, dir, "update-ref", "refs/heads/main", run(t, dir, "commit-tree", "-p", "refs/heads/main",
		"-m", "base moved", run(t, dir, "rev-parse", "refs/heads/main^{tree}")))

	r, _ := gitrepo.Open(dir)
	got, ok, err := Choose(r, "main")
	if err != nil || !ok {
		t.Fatalf("Choose: ok=%v err=%v", ok, err)
	}
	if got.Working != topicHead {
		t.Fatalf("working = %s, want the topic parent %s", got.Working, topicHead)
	}
	if got.SHA != fork {
		t.Fatalf("previous = %s, want the fork point %s", got.SHA, fork)
	}
}

func TestFirstCommitHasNothingToCompare(t *testing.T) {
	dir := repo(t)
	commit(t, dir, "a.txt", "one", "first")
	r, _ := gitrepo.Open(dir)
	if _, ok, err := Choose(r, "main"); ok || err != nil {
		t.Fatalf("ok=%v err=%v, want nothing to compare", ok, err)
	}
}

// A merge of two branches that have both already landed on the base has
// nothing outside the base to compare: both parents are ancestors of the
// base, and Choose must say so rather than falling through to MergeBase and
// comparing HEAD against one of its own parents.
func TestReMergeOfTwoLandedBranchesHasNothingToCompare(t *testing.T) {
	dir := repo(t)
	commit(t, dir, "a.txt", "one", "first")

	run(t, dir, "checkout", "-q", "-b", "topicA")
	topicA := commit(t, dir, "a.txt", "two", "topicA work")
	run(t, dir, "checkout", "-q", "main")
	run(t, dir, "merge", "-q", "--no-ff", "-m", "merge topicA", "topicA")

	run(t, dir, "checkout", "-q", "-b", "topicB")
	topicB := commit(t, dir, "b.txt", "three", "topicB work")
	run(t, dir, "checkout", "-q", "main")
	run(t, dir, "merge", "-q", "--no-ff", "-m", "merge topicB", "topicB")

	// Both topicA and topicB are now ancestors of main. Re-merge them
	// together while detached, so HEAD itself is off the base but both of
	// its parents are on it.
	run(t, dir, "checkout", "-q", "--detach", topicA)
	run(t, dir, "merge", "-q", "--no-ff", "-m", "re-merge", topicB)

	r, _ := gitrepo.Open(dir)
	if _, ok, err := Choose(r, "main"); ok || err != nil {
		t.Fatalf("ok=%v err=%v, want nothing to compare", ok, err)
	}
}

// TestAgainstUsesTheRevisionDirectly: --against names the previous revision
// directly, with no merge-base computed at all — SHA is exactly the ref
// handed in, Working is HEAD, regardless of where that ref sits in history.
func TestAgainstUsesTheRevisionDirectly(t *testing.T) {
	dir := repo(t)
	first := commit(t, dir, "a.txt", "one", "first")
	head := commit(t, dir, "b.txt", "two", "second")

	r, _ := gitrepo.Open(dir)
	got, err := Against(r, first)
	if err != nil {
		t.Fatalf("Against: %v", err)
	}
	if got.SHA != first {
		t.Fatalf("SHA = %s, want the named revision %s", got.SHA, first)
	}
	if got.Working != head {
		t.Fatalf("Working = %s, want HEAD %s", got.Working, head)
	}
}
