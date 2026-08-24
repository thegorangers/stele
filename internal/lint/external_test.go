package lint_test

import (
	"testing"

	"github.com/thegorangers/stele/internal/lint"
	"github.com/thegorangers/stele/rule"
)

// outOfTree is written the way a rule in somebody else's repository is
// written: against the published package, with no access to anything under
// internal/.
type outOfTree struct{}

func (outOfTree) ID() string          { return "example/out_of_tree" }
func (outOfTree) Description() string { return "is written outside this repository" }
func (outOfTree) Check(f rule.File) ([]rule.Finding, error) {
	return nil, nil
}

// TestOutOfTreeRuleSatisfiesTheEngineInterface is the whole claim of slice 1
// made checkable: the interface a rule outside this repository implements and
// the interface the engine runs are one interface, not two.
func TestOutOfTreeRuleSatisfiesTheEngineInterface(t *testing.T) {
	var r lint.Rule = outOfTree{}
	if r.ID() != "example/out_of_tree" {
		t.Fatalf("ID() = %q", r.ID())
	}
}
