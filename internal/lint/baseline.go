package lint

// The baseline: what this repository already knows about, so that a run can
// fail on what it does not.
//
// # Why it exists
//
// `severity: warning` buys time to fix what is there. It does not protect new
// code from the same mistake: a repository with 111 fields named `_at` gets
// the same line about the 112th, written tomorrow, and nothing in the output
// separates them. The roll-up made that comfortable rather than better — a
// line saying "111 warnings in 19 files" is a line somebody can read every day
// without ever reading behind it, which is `allow_failure: true` with tidier
// output. A baseline is the mechanism that makes an existing count survivable
// while a new violation still fails.
//
// # What an entry identifies, and why not the line
//
// (file, rule, line) is the obvious answer and it is wrong. Inserting a line
// above a finding moves every finding below it, so the baseline goes stale on
// an edit that changed nothing it is about, and people regenerate it
// reflexively — which is the same as not having one, except that it now also
// launders new findings into it on every regeneration.
//
// An entry is (import path, rule, subject, count): the full name of the
// declaration the finding is about, which survives reformatting, reordering
// and insertion, and stops being the same name exactly when the declaration
// stops being the same declaration. See rule.Finding.Subject for why the
// engine derives it rather than the rule stating it.
//
// The count is there because a subject is not always unique to a finding. A
// finding about the file as a whole has no declaration to name, so its subject
// is empty, and two such findings from one rule would otherwise be one entry
// that silently covered both. Recording how many there were means the second
// one appearing tomorrow is a new finding, which is the only thing this file
// exists to detect. An absent count is one.
//
// # Why it is a file and not a manifest block
//
// The manifest is what a repository *intends*: which rules, at what cost, over
// which paths, in six lines somebody reads. The baseline is what a repository
// *happens to have*, in as many lines as it has findings, and a hundred of
// them in stele.yaml would drown the six. It is the lock's situation exactly —
// a generated file, committed, read in review, written only when a flag asks —
// so it is the lock's shape: deterministic bytes, validated before writing,
// and never produced by an ordinary run.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/thegorangers/stele/internal/atomicfile"
	"gopkg.in/yaml.v3"
)

// BaselineName is the file a lint run reads its baseline from, beside the
// manifest and the lock.
const BaselineName = "stele.baseline"

// BaselineVersion is the only baseline format version this tool understands.
// It is its own number, as the lock's is: the three files evolve
// independently, and one number shared between them would force a change in
// any of them through all three.
const BaselineVersion = 1

// Baseline is a parsed stele.baseline: the findings this repository has
// already accepted, so that a run fails on the ones it has not.
type Baseline struct {
	// Version of the baseline format.
	Version int `yaml:"version"`
	// Findings are the accepted findings, in a stable order.
	Findings []BaselineEntry `yaml:"findings"`
}

// BaselineEntry is one accepted finding, identified by what survives an edit.
//
// There is deliberately no message, no fix and no line. A message is prose
// that a rule may reword in a patch release, and an entry that stopped
// matching because somebody improved a sentence would be a new failure with no
// cause. A line is the identity this file exists to avoid.
type BaselineEntry struct {
	// Rule is the id of the rule that reported it. It is the same permanent
	// public identifier that goes in lint.rules, and an entry naming a rule
	// no loaded rule carries is an error naming it — for the same reason
	// lint.rules is: the repository believes it has an exemption it does not
	// have, and this is the only moment anybody would find out.
	Rule string `yaml:"rule"`
	// Path is the import path of the file, as the rules and the ignore list
	// see it, not the path on disk. Two repositories in one closure can
	// supply one disk layout under different import paths, and the import
	// path is the name the finding was reported under.
	Path string `yaml:"path"`
	// Subject is the full name of the declaration, or empty for a finding
	// about the file as a whole.
	Subject string `yaml:"subject,omitempty"`
	// Count is how many findings of this rule were made about this subject.
	// Absent means one, which is what it almost always is; writing `count: 1`
	// on every line of a file people read would be noise in every line.
	Count int `yaml:"count,omitempty"`
}

// key is what identifies an entry, and what a finding is matched against.
// The separator cannot occur in any part: an import path has no spaces, a
// rule id is namespace/name, and a full name is dotted identifiers.
func (e BaselineEntry) key() string { return e.Path + " " + e.Rule + " " + e.Subject }

// entryFor is the entry a finding would be recorded as, before counting.
func entryFor(f Finding) BaselineEntry {
	return BaselineEntry{Rule: f.Rule, Path: f.Path, Subject: f.Subject, Count: 1}
}

// String renders an entry the way the rest of the output renders a finding,
// minus the position it deliberately does not hold. It is what a stale entry
// is reported as.
func (e BaselineEntry) String() string {
	s := e.Path
	if e.Subject != "" {
		s += ": " + e.Subject
	}
	s += ": " + e.Rule
	if e.Count > 1 {
		s += fmt.Sprintf(" (%d)", e.Count)
	}
	return s
}

// LoadBaseline reads and validates the baseline at path.
//
// Parsing is strict, as it is for the manifest and the lock: a key outside the
// format is an error naming that key. A file the reader cannot make sense of
// is not a file it should guess about — the guess would be a suppression
// nobody wrote.
func LoadBaseline(path string) (*Baseline, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true) // an unknown key is an error naming that key
	var b Baseline
	if err := dec.Decode(&b); err != nil && err != io.EOF {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := b.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i := range b.Findings {
		if b.Findings[i].Count == 0 {
			b.Findings[i].Count = 1
		}
	}
	return &b, nil
}

func (b *Baseline) validate() error {
	switch {
	case b.Version == 0:
		return fmt.Errorf("version: missing; expected %d", BaselineVersion)
	case b.Version != BaselineVersion:
		return fmt.Errorf("version: %d is not supported; expected %d", b.Version, BaselineVersion)
	}
	seen := make(map[string]int, len(b.Findings))
	for i, e := range b.Findings {
		switch {
		case e.Rule == "":
			return fmt.Errorf("findings[%d].rule: missing", i)
		case e.Path == "":
			return fmt.Errorf("findings[%d].path: missing for rule %q", i, e.Rule)
		case e.Count < 0:
			return fmt.Errorf("findings[%d].count: %d is not a count", i, e.Count)
		}
		if err := CheckID(e.Rule); err != nil {
			return fmt.Errorf("findings[%d].rule: %w", i, err)
		}
		// One subject recorded twice is two answers to one lookup, and the
		// counts would each be a different claim about the same thing. It is
		// the lock's duplicate-key refusal, for the same reason.
		if j, ok := seen[e.key()]; ok {
			return fmt.Errorf("findings[%d]: %s is recorded twice, by findings[%d] and findings[%d]; "+
				"an entry holds a count rather than repeating. Delete one and re-derive the file with "+
				"`stele lint --update-baseline`, which rewrites the whole of it from one run",
				i, e, j, i)
		}
		seen[e.key()] = i
	}
	return nil
}

// SaveBaseline writes b to path.
//
// Entries are sorted by path, then rule, then subject, so that re-deriving a
// baseline that changed nothing produces an identical file and an empty diff.
// The file is validated before it is written: this tool does not emit a file
// it would refuse to read, and holding that here holds it for every writer
// there will ever be rather than for the ones somebody remembered.
func SaveBaseline(path string, b *Baseline) error {
	out := Baseline{Version: b.Version, Findings: append([]BaselineEntry(nil), b.Findings...)}
	if out.Version == 0 {
		out.Version = BaselineVersion
	}
	sort.Slice(out.Findings, func(i, j int) bool {
		a, c := out.Findings[i], out.Findings[j]
		switch {
		case a.Path != c.Path:
			return a.Path < c.Path
		case a.Rule != c.Rule:
			return a.Rule < c.Rule
		default:
			return a.Subject < c.Subject
		}
	})
	if err := out.validate(); err != nil {
		return fmt.Errorf("%s: refusing to write a baseline this tool could not read: %w", path, err)
	}
	// A count of one is what an entry already means.
	for i := range out.Findings {
		if out.Findings[i].Count == 1 {
			out.Findings[i].Count = 0
		}
	}
	var buf bytes.Buffer
	buf.WriteString("# Generated by stele. Edited by the tool, reviewed by people.\n" +
		"#\n" +
		"# These findings were already here when the baseline was taken. A run does not\n" +
		"# fail on them; it fails on findings that are not in this file. Fix one and\n" +
		"# re-derive with `stele lint --update-baseline`; the file is meant to shrink.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return atomicfile.Write(path, buf.Bytes())
}

// BaselineFrom derives a baseline from what a run found: every finding, at the
// rule and subject it was made about, counted.
//
// It records findings only, never failures. A rule that could not run has said
// nothing about this repository, and an entry standing for its silence would
// be an accepted finding nobody ever saw.
func BaselineFrom(res Result) *Baseline {
	counts := make(map[string]*BaselineEntry, len(res.Findings))
	order := make([]string, 0, len(res.Findings))
	for _, f := range res.Findings {
		e := entryFor(f)
		if prev, ok := counts[e.key()]; ok {
			prev.Count++
			continue
		}
		counts[e.key()] = &e
		order = append(order, e.key())
	}
	b := &Baseline{Version: BaselineVersion, Findings: make([]BaselineEntry, 0, len(order))}
	for _, k := range order {
		b.Findings = append(b.Findings, *counts[k])
	}
	return b
}

// known returns how many findings of each identity the baseline accepts, and
// refuses a baseline naming a rule that is not loaded.
//
// The refusal is the manifest's, in the same words, because it is the same
// mistake with the same consequence: a repository that believes it has an
// exemption it does not have, and a build that reddens for a reason nothing in
// the repository explains.
func (b *Baseline) known(byID map[string]bool) (map[string]int, error) {
	if b == nil {
		return nil, nil
	}
	out := make(map[string]int, len(b.Findings))
	var unknown []string
	for _, e := range b.Findings {
		if !byID[e.Rule] && !contains(unknown, e.Rule) {
			unknown = append(unknown, e.Rule)
		}
		n := e.Count
		if n == 0 {
			n = 1
		}
		out[e.key()] += n
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("lint: %s holds findings for %s, which is not a rule this tool has; "+
			"run `stele lint --rules` for the list. A baseline naming a rule nobody loads is an "+
			"exemption this repository believes it has and does not",
			BaselineName, strings.Join(unknown, ", "))
	}
	return out, nil
}
