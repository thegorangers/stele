// Package lint checks proto contracts against rules, and reports what it
// finds without changing anything.
//
// # What a rule is, and why the interface is this shape
//
// A Rule is handed one linked file and returns what it has to say about it.
// That is the whole contract, and it is deliberately the contract an
// out-of-tree rule would implement rather than a convenience for the rules
// that happen to ship here: the plugin host, when it lands, is a transport for
// this interface and not a second one. Nothing in Rule reaches the filesystem,
// the manifest, the configuration or the process, because none of those
// survive a process boundary. What crosses it is a set of descriptors and a
// list of findings, which is exactly what Check takes and returns.
//
// Two things a rule deliberately does not decide:
//
//   - Its own severity. Severity is the reader's judgement about their own
//     repository, not the rule author's about somebody else's, and it is what
//     lets a repository switch a rule on before it has fixed everything the
//     rule finds. A rule states what it requires; configuration states what
//     happens when it is not met.
//
//   - Where a finding is reported from. A rule names the descriptor; the
//     engine turns that into a rule ID, an import path and a position. A rule
//     that stamped its own ID could stamp somebody else's.
//
// # What this package will not do
//
// Rules here are mechanical: decidable from the descriptor, with no knowledge
// of what the author meant. Most of API design is not, and a rule that is
// right most of the time is worse than no rule — it teaches the reader that
// the output is noise, and the next reader switches it off. Where a check
// would need intent, this package says so rather than approximating it. See
// the roadmap for the ones deliberately left out and why.
package lint

import (
	"fmt"
	"sort"
	"strings"

	"github.com/thegorangers/stele/rule"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The vocabulary a rule is written in lives in the published rule package,
// because a rule in somebody else's repository has to be able to import it and
// this one is internal. These are aliases rather than conversions on purpose:
// an out-of-tree rule and a built-in satisfy the same interface, by identity,
// and the host is a transport for it rather than a second interface.
type (
	// Severity says what a finding costs. See rule.Severity.
	Severity = rule.Severity
	// Position is where in a file a finding is. See rule.Position.
	Position = rule.Position
	// Finding is one thing a rule has to say about one file. See rule.Finding.
	Finding = rule.Finding
	// File is what a rule is given. See rule.File.
	File = rule.File
	// Rule is one check over one file. See rule.Rule.
	Rule = rule.Rule
)

// Severities, re-exported so that the engine's callers need not import two
// packages to name one.
const (
	SeverityError   = rule.SeverityError
	SeverityWarning = rule.SeverityWarning
	SeverityOff     = rule.SeverityOff

	SeverityNameError   = rule.SeverityNameError
	SeverityNameWarning = rule.SeverityNameWarning
	SeverityNameOff     = rule.SeverityNameOff

	// NamespaceBuiltin is the rule namespace reserved for this tool.
	NamespaceBuiltin = rule.NamespaceBuiltin
	// NamespaceAIP is the rule namespace reserved for the AIP rules.
	NamespaceAIP = rule.NamespaceAIP
)

// ParseSeverity reads the spelling configuration uses.
func ParseSeverity(s string) (Severity, error) { return rule.ParseSeverity(s) }

// CheckID reports whether id is well formed: `namespace/name`.
func CheckID(id string) error { return rule.CheckID(id) }

// RuleConfig is what a repository says about one rule.
type RuleConfig struct {
	// Severity is what a finding of this rule costs.
	Severity Severity
	// Ignore lists import paths this rule is not applied to. See Config.
	Ignore []string
}

// Config is what a repository says about linting.
type Config struct {
	// Ignore lists import paths no rule is applied to.
	//
	// An entry is an import path or a prefix of one, never a glob. There is
	// no glob dialect to learn, none to get subtly wrong, and no question
	// about whether `*` crosses a directory separator: `a/b/c.proto` names
	// one file and `a/b` names everything under it.
	Ignore []string
	// Rules is what is said about individual rules, by ID. A rule not named
	// here runs at DefaultSeverity for its namespace over every path.
	Rules map[string]RuleConfig
	// Baseline is what this repository has already accepted, read from
	// stele.baseline. Nil means every finding costs what its severity says.
	//
	// It is not part of the manifest and it is not per-rule configurable. A
	// per-rule switch would be a second severity axis, and severity already
	// decides which rules are worth holding: a rule a repository intends to
	// fix is held at error and baselined, a rule it has not decided about is
	// a warning, and a rule it will never apply is off or ignored.
	//
	// It is also not `ignore`, and the difference is the one that keeps both
	// worth having. `ignore` is prospective and unbounded — this rule does
	// not apply to these paths, including the file somebody adds tomorrow —
	// and it is written by hand in a manifest, where it is reviewed as
	// intent. A baseline is retrospective and exhaustive: these exact
	// declarations violate this rule today, nothing else does, and the file
	// is meant to shrink. An exemption you never intend to revisit belongs in
	// the manifest; debt you intend to pay belongs here.
	Baseline *Baseline
	// Only, when non-empty, narrows the run to the rules it names. It is not
	// configuration a manifest can hold: it is what `stele lint --rule` asks
	// for at a terminal, to read one rule's findings after the report rolled
	// them up into a count. A manifest that could narrow a run this way would
	// be a manifest that could switch every other rule off in one line
	// nobody reviewing it would read as switching anything off.
	Only []string
}

// DefaultSeverity is what a finding of a rule the manifest says nothing about
// costs.
//
// It is derived from the namespace rather than stated by the rule, because a
// rule does not decide its own severity: the published interface says so, and
// an out-of-tree rule that could ship itself at error would be deciding
// something about somebody else's repository. What the namespace says is not
// the rule author's opinion; it is this tool's, about a group of rules it
// ships, and it is one line rather than a table that can drift from the rules
// it describes.
//
// Two answers, and the asymmetry is argued in docs/AIP.md §6:
//
//   - The stele rules fail. Each was measured against a real fleet before it
//     shipped, and each cost that fleet nearly nothing, so a finding is
//     evidence of a mistake rather than of a different tradition.
//
//   - The aip rules warn. They are guidance, they were measured against the
//     same fleet in the hundreds, and a rule that reddens every repository on
//     upgrade is a rule that gets switched off — along with whatever is next
//     to it. A repository that has fixed its contracts raises them to error in
//     stele.yaml, which is the ordinary knob and not a special one.
func DefaultSeverity(id string) Severity {
	if rule.Namespace(id) == NamespaceAIP {
		return SeverityWarning
	}
	return SeverityError
}

// contains reports whether ids holds id.
func contains(ids []string, id string) bool {
	for _, s := range ids {
		if s == id {
			return true
		}
	}
	return false
}

// ignores reports whether path is covered by one of the entries.
func ignores(entries []string, path string) bool {
	for _, e := range entries {
		e = strings.TrimSuffix(e, "/")
		if e == "" {
			continue
		}
		if path == e || strings.HasPrefix(path, e+"/") {
			return true
		}
	}
	return false
}

// Engine runs a set of rules under a configuration.
type Engine struct {
	rules []Rule
	cfg   Config
	// known is how many findings of each baseline identity are accepted. It
	// is resolved once, in New, because that is where a baseline naming a
	// rule nobody loads has to be refused.
	known map[string]int
}

// New binds rules to a configuration.
//
// A configured rule that no rule in the set carries is an error naming it, not
// a line that does nothing. The two ways that happens are a typo and a rule
// that has been removed, and both mean the repository believes it has a
// protection or an exemption it does not have. Failing here is the only moment
// anybody would find out.
//
// Two rules with one ID is likewise an error: whichever ran, the other's
// configuration would silently describe nothing.
func New(rules []Rule, cfg Config) (*Engine, error) {
	byID := make(map[string]bool, len(rules))
	for _, r := range rules {
		id := r.ID()
		if err := CheckID(id); err != nil {
			return nil, fmt.Errorf("lint: %w", err)
		}
		if byID[id] {
			return nil, fmt.Errorf("lint: two rules claim the ID %q; an ID names exactly one rule", id)
		}
		byID[id] = true
	}
	var unknown []string
	for id := range cfg.Rules {
		if !byID[id] {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		known := make([]string, 0, len(byID))
		for id := range byID {
			known = append(known, id)
		}
		sort.Strings(known)
		return nil, fmt.Errorf("lint: configured for %s, which is not a rule this tool has; "+
			"run `stele lint --rules` for the list (known: %s)",
			strings.Join(unknown, ", "), strings.Join(known, ", "))
	}
	var missing []string
	for _, id := range cfg.Only {
		if !byID[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("lint: asked for %s, which is not a rule this tool has; "+
			"run `stele lint --rules` for the list", strings.Join(missing, ", "))
	}
	known, err := cfg.Baseline.known(byID)
	if err != nil {
		return nil, err
	}
	sorted := append([]Rule(nil), rules...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID() < sorted[j].ID() })
	return &Engine{rules: sorted, cfg: cfg, known: known}, nil
}

// Result is everything one run of the engine found.
type Result struct {
	// Findings are in a deterministic order: by import path, then by rule,
	// then by position. Two runs over the same files produce the same bytes,
	// which is what makes a lint report diffable.
	Findings []Finding
	// Errors and Warnings count the findings by what they cost, so that a
	// summary does not have to re-derive it. A baselined finding is in
	// neither: it costs nothing, which is what baselining it meant.
	Errors   int
	Warnings int
	// Baselined counts the findings the baseline held. It is separate rather
	// than folded into the two above because it is the number that says how
	// much of a green run was earned and how much was bought, and a summary
	// that did not print it would be the roll-up's failure again: a
	// comfortable line with an unread count behind it.
	Baselined int
	// Stale are the baseline entries nothing found this run, in the file's
	// order.
	//
	// They are reported and they do not fail the run. Keeping them silently
	// is how a baseline rots from a record of debt into a standing
	// permission; failing on them is how fixing a finding reddens the build
	// of the person who fixed it, and a repository learns quickly not to fix
	// things. Reporting them puts the cost where it belongs — one line and
	// one command — and `--update-baseline` is what drops them.
	Stale []BaselineEntry
	// Files is how many files were checked. A run that checked nothing looks
	// exactly like a clean run without it.
	Files int
	// Rules is how many rules were applied to at least one file.
	Rules int
	// Failures are the rules that could not reach an answer at all, one per
	// rule and file that failed.
	//
	// They are collected rather than returned at the first one because a run
	// that reports one broken rule and stops hides the second, and because
	// the findings the other rules did produce are still worth printing. What
	// they must never do is disappear: a rule that failed to run is not a
	// rule that found nothing, and a build that went green because a rule
	// crashed is the exact failure this whole tool exists to remove.
	Failures []Failure
}

// Failure is one rule that could not answer for one file.
type Failure struct {
	// Rule is the ID of the rule that failed.
	Rule string
	// Path is the import path of the file it was checking.
	Path string
	// Err is what went wrong, already naming the rule and what to do.
	Err error
}

// String renders a failure in the same shape a finding takes, so that the two
// read as one stream in a CI log.
func (f Failure) String() string {
	return fmt.Sprintf("%s: %s: %s: did not run: %v", f.Path, SeverityNameError, f.Rule, f.Err)
}

// Check runs every rule over every file.
func (e *Engine) Check(files []protoreflect.FileDescriptor) Result {
	res := Result{Files: len(files)}
	applied := make(map[string]bool)
	// A copy, because Check must be able to run twice on one engine and give
	// the same answer both times.
	remaining := make(map[string]int, len(e.known))
	for k, n := range e.known {
		remaining[k] = n
	}
	for _, fd := range files {
		path := string(fd.Path())
		if ignores(e.cfg.Ignore, path) {
			res.Files--
			continue
		}
		f := File{Desc: fd}
		for _, r := range e.rules {
			if len(e.cfg.Only) > 0 && !contains(e.cfg.Only, r.ID()) {
				continue
			}
			rc, configured := e.cfg.Rules[r.ID()]
			if configured && (rc.Severity == SeverityOff || ignores(rc.Ignore, path)) {
				continue
			}
			sev := rc.Severity
			if !configured {
				sev = DefaultSeverity(r.ID())
			}
			applied[r.ID()] = true
			found, err := r.Check(f)
			if err != nil {
				res.Failures = append(res.Failures, Failure{Rule: r.ID(), Path: path, Err: err})
				continue
			}
			for _, fi := range found {
				fi.Rule = r.ID()
				fi.Path = path
				fi.Subject = subjectOf(f, fi.Pos)
				fi.Severity = sev
				if fi.Severity != SeverityWarning {
					fi.Severity = SeverityError
				}
				// The baseline is consulted after severity is resolved and
				// not before: what a finding would have cost is part of what
				// is reported about it, and a finding held by the baseline
				// has to keep saying which of the two it is.
				if k := entryFor(fi).key(); remaining[k] > 0 {
					remaining[k]--
					fi.Baselined = true
					res.Baselined++
				} else if fi.Severity == SeverityWarning {
					res.Warnings++
				} else {
					res.Errors++
				}
				res.Findings = append(res.Findings, fi)
			}
		}
	}
	res.Rules = len(applied)
	// What the baseline accepted and nothing produced. Reported in the
	// file's order, so the reader can find the lines to delete.
	if e.cfg.Baseline != nil {
		for _, entry := range e.cfg.Baseline.Findings {
			if n := remaining[entry.key()]; n > 0 {
				stale := entry
				stale.Count = n
				res.Stale = append(res.Stale, stale)
			}
		}
	}
	sort.SliceStable(res.Findings, func(i, j int) bool {
		a, b := res.Findings[i], res.Findings[j]
		switch {
		case a.Path != b.Path:
			return a.Path < b.Path
		case a.Pos.Line != b.Pos.Line:
			return a.Pos.Line < b.Pos.Line
		case a.Pos.Col != b.Pos.Col:
			return a.Pos.Col < b.Pos.Col
		default:
			return a.Rule < b.Rule
		}
	})
	return res
}

// lowerSnake reports whether s is lower_snake_case and starting with a letter.
// It is the shape check the built-in rules apply to proto identifiers; the
// same shape is applied to rule IDs by rule.CheckID.
func lowerSnake(s string) error {
	if s == "" {
		return fmt.Errorf("is empty")
	}
	if s[0] < 'a' || s[0] > 'z' {
		return fmt.Errorf("must start with a lower-case letter")
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return fmt.Errorf("must be lower_snake_case")
		}
	}
	return nil
}
