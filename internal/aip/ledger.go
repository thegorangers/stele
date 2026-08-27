package aip

import (
	"bytes"
	_ "embed"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed index.tsv
var indexTSV []byte

//go:embed ledger.yaml
var ledgerYAML []byte

// Snapshot returns the committed inventory of the corpus.
func Snapshot() (Index, error) { return ParseIndex(bytes.NewReader(indexTSV)) }

// Disposition is what has been decided about one AIP.
//
// There are four decisions and no fifth, because every additional word is
// somewhere for an undecided AIP to hide. `untriaged` is not a decision — it
// is a maintainer's note that an AIP has been read and not decided, and it
// fails the ledger test on sight. Nothing generates it: an AIP nobody has
// written a line about has no entry at all, and that fails too. The two
// failures are deliberately the same colour.
type Disposition string

const (
	// Implemented: rules exist. Rules must name them, and each must be a rule
	// this tool actually loads.
	Implemented Disposition = "implemented"
	// Candidate: decidable from a FileDescriptorProto, no rule yet. Reason
	// says what the rule would check and, where it has been measured, what it
	// would find.
	Candidate Disposition = "candidate"
	// Declined: deliberately no rule. The reason says why, and there are two
	// kinds: an AIP that states no requirement about a file at all (the
	// program's own process, or a vendor's platform), and a decidable check
	// that was measured and rejected.
	Declined Disposition = "declined"
	// Undecidable: not answerable from a descriptor. Reason says what the
	// missing knowledge is.
	Undecidable Disposition = "undecidable"
	// Untriaged: read and not decided. It is always a failing state; it
	// exists so that "I have looked at this and I am not ready" can be
	// written down without pretending to be a decision.
	Untriaged Disposition = "untriaged"
)

// Entry is one line of the ledger.
type Entry struct {
	// ID is the AIP number. It is the join with the derived index, which is
	// why the ledger stores nothing else upstream owns: a title copied here
	// would be a second place for upstream's words to be wrong.
	ID int `yaml:"id"`
	// State is the disposition.
	State Disposition `yaml:"state"`
	// Rules names the rule IDs that implement this AIP, wholly or partly. It
	// is required for `implemented` and allowed on any other state, where it
	// means "these existing rules cover part of it".
	Rules []string `yaml:"rules,omitempty"`
	// Reason is why, in one sentence. Required for everything but
	// `implemented`, whose reason is the rules it names.
	Reason string `yaml:"reason,omitempty"`
}

type ledgerFile struct {
	Version int     `yaml:"version"`
	AIPs    []Entry `yaml:"aips"`
}

// LedgerVersion is the only ledger format this tool understands.
const LedgerVersion = 1

// LoadLedger returns the committed ledger, in ID order.
func LoadLedger() ([]Entry, error) { return parseLedger(ledgerYAML) }

func parseLedger(b []byte) ([]Entry, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var f ledgerFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("ledger.yaml: %w", err)
	}
	if f.Version != LedgerVersion {
		return nil, fmt.Errorf("ledger.yaml: version %d is not supported; expected %d", f.Version, LedgerVersion)
	}
	seen := make(map[int]bool, len(f.AIPs))
	for i, e := range f.AIPs {
		switch {
		case e.ID == 0:
			return nil, fmt.Errorf("ledger.yaml: aips[%d] has no id", i)
		case seen[e.ID]:
			return nil, fmt.Errorf("ledger.yaml: AIP %d has two entries", e.ID)
		}
		seen[e.ID] = true
		switch e.State {
		case Implemented:
			if len(e.Rules) == 0 {
				return nil, fmt.Errorf("ledger.yaml: AIP %d is implemented but names no rule", e.ID)
			}
		case Candidate, Declined, Undecidable:
			if strings.TrimSpace(e.Reason) == "" {
				return nil, fmt.Errorf("ledger.yaml: AIP %d is %s and gives no reason", e.ID, e.State)
			}
		case Untriaged:
			// Accepted by the parser, refused by the test. The parser is not
			// the gate; the test is, and it says the same thing about this
			// entry as it says about a missing one.
		default:
			return nil, fmt.Errorf("ledger.yaml: AIP %d has state %q, which is not a disposition", e.ID, e.State)
		}
	}
	out := append([]Entry(nil), f.AIPs...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
