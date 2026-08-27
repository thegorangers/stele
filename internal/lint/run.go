package lint

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/thegorangers/stele/internal/compile"
	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/lint/host"
	"github.com/thegorangers/stele/internal/pin"
	"github.com/thegorangers/stele/internal/plugin"
	"github.com/thegorangers/stele/internal/resolve"
	"github.com/thegorangers/stele/internal/source"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ManifestName and LockName are the files a lint run reads. They are the same
// two files a generation reads, and deliberately so: a lint that judged a
// different closure from the one that generates the code would be measuring a
// contract nobody ships.
const (
	ManifestName = "stele.yaml"
	LockName     = "stele.lock"
)

// BaselineName is declared in baseline.go, beside the format it names.

// Options configures one lint run.
type Options struct {
	// Dir is the module root: the directory holding stele.yaml. Empty means
	// ".".
	Dir string
	// Update re-resolves every ref and rewrites the lock, exactly as
	// generate's flag of the same name does.
	Update bool
	// NoLock leaves the lock out of the run entirely.
	NoLock bool
	// Rules are the rules to run. Nil means Builtin.
	Rules []Rule
	// Warn, when set, receives non-fatal findings from resolution.
	Warn io.Writer
	// Fetch materialises dependency repositories. When nil, repositories are
	// fetched from the network into CacheRoot.
	Fetch resolve.FetchFunc
	// Only, when non-empty, narrows the run to the rules it names and prints
	// every finding they make. It is what `stele lint --rule` passes.
	Only []string
	// UpdateBaseline rewrites stele.baseline from what this run finds, and
	// makes the run itself pass.
	//
	// It is a flag rather than a default because a baseline written by an
	// ordinary run would turn every red build green by running it twice, and
	// because the file is meant to be read in a review: something a person
	// asked for, once, is something a reviewer can see was asked for.
	//
	// The rewrite is total. It drops the entries nothing found, which is the
	// only way the file shrinks, and it records what is there now — so a run
	// narrowed to one rule is refused by the caller before it reaches here.
	UpdateBaseline bool
	// CacheRoot is where fetched repositories and installed plugins are kept.
	// Required when Fetch is nil and the manifest declares dependencies, and
	// when it declares a rule plugin this tool has to install or download.
	CacheRoot string
}

// ConfigFrom translates a manifest's lint block into the engine's
// configuration.
//
// It lives here rather than on config.Lint because the dependency runs this
// way: the engine reads manifests, so a manifest parser that read the engine
// would be a cycle. A nil block is an empty configuration rather than an
// error, so that no caller has to test for absence.
func ConfigFrom(l *config.Lint) Config {
	var c Config
	if l == nil {
		return c
	}
	c.Ignore = l.Ignore
	if len(l.Rules) == 0 {
		return c
	}
	c.Rules = make(map[string]RuleConfig, len(l.Rules))
	for _, r := range l.Rules {
		// The severity is already known to parse: the manifest refused
		// anything else before this was reachable.
		sev := SeverityError
		if r.Severity != "" {
			sev, _ = ParseSeverity(r.Severity)
		}
		c.Rules[r.ID] = RuleConfig{Severity: sev, Ignore: r.Ignore}
	}
	return c
}

// Report is what one lint run produced.
type Report struct {
	Result
	// Notes are things about this run that qualify everything below them but
	// are not findings: at present, the rule plugins nothing pins. They are
	// printed above the findings for that reason — a reader who meets them
	// afterwards has already read the findings as the whole answer.
	Notes []string
	// Updated says this run rewrote the baseline. A run that did costs
	// nothing whatever it found: what it found is now the baseline.
	Updated bool
	// Detail names the rules whose findings are printed one to a line
	// however many there are. It is what `stele lint --rule` sets. Empty
	// means the threshold below decides.
	Detail []string
	// disk maps an import path to the path on disk it was read from, so that
	// output points at a file a reader can open rather than at a name only
	// the resolver uses.
	disk map[string]string
}

// Failed reports whether the run should fail the build: an error-severity
// finding, or a rule that could not run at all. A warning is reported and
// costs nothing, which is the entire point of it.
//
// A failed rule fails the run regardless of what severity that rule was
// configured at. Severity says what a *finding* costs; a rule that never
// reached a finding has said nothing about this repository, and a run that
// passed on its silence would be reporting an absence of evidence as evidence
// of absence.
func (r *Report) Failed() bool {
	if r.Updated {
		return len(r.Failures) > 0
	}
	return r.Errors > 0 || len(r.Failures) > 0
}

// Write renders every finding, in order, one to a line with its fix indented
// beneath.
//
// The path is the file on disk, relative to the directory the run was pointed
// at. The import path is what the rules see and what the ignore list matches,
// but it is not a file anybody can open: a dependency's `example/v1/a.proto`
// and this repository's are the same import path and different files, and a
// reader in a CI log has to know which one to go to.
func (r *Report) Write(w io.Writer) {
	for _, n := range r.Notes {
		fmt.Fprintln(w, n)
	}
	// Failures first. They are the reason the rest of the output is
	// incomplete, and a reader who sees the findings first reads them as the
	// whole answer.
	for _, f := range r.Failures {
		if p, ok := r.disk[f.Path]; ok {
			f.Path = p
		}
		fmt.Fprintln(w, f.String())
	}
	live, held := r.rollUp()
	for _, f := range r.Findings {
		rolled := live
		if f.Baselined {
			rolled = held
		}
		if rolled[f.Rule] > 0 {
			continue
		}
		if p, ok := r.disk[f.Path]; ok {
			f.Path = p
		}
		fmt.Fprintln(w, f.String())
	}
	for _, id := range sortedKeys(live) {
		fmt.Fprintln(w, r.rollUpLine(id, false))
	}
	for _, id := range sortedKeys(held) {
		fmt.Fprintln(w, r.rollUpLine(id, true))
	}
	for _, line := range r.staleLines() {
		fmt.Fprintln(w, line)
	}
}

// staleLines reports the baseline entries nothing found.
//
// They are never a failure — a repository that reddened its build by fixing a
// finding learns not to fix findings — and they are never silent either, or
// the file only ever grows and a record of debt becomes a standing permission.
// The volume rule is the roll-up's, for the same reason: a few are worth
// naming so the reader can see which lines to delete, and forty are a count
// and a command.
func (r *Report) staleLines() []string {
	if len(r.Stale) == 0 {
		return nil
	}
	if len(r.Stale) > SummaryThreshold {
		return []string{fmt.Sprintf("stele: %s: %s that nothing found this run; "+
			"drop them with `stele lint --update-baseline`. Findings that are fixed are the point",
			BaselineName, entries(len(r.Stale)))}
	}
	out := make([]string, 0, len(r.Stale))
	for _, e := range r.Stale {
		disk := e.Path
		if p, ok := r.disk[e.Path]; ok {
			disk = p
		}
		shown := e
		shown.Path = disk
		out = append(out, fmt.Sprintf("stele: %s: nothing found %s any more; "+
			"drop it with `stele lint --update-baseline`", BaselineName, shown))
	}
	return out
}

// SummaryThreshold is how many warnings one rule may report before the report
// prints a count instead of the findings.
//
// The number is not load-bearing and it is not tuned; what matters is that
// there is one. Below it the detail is cheaper than the round trip to fetch
// it, above it the detail is a page of output that answers a question the
// reader did not ask. Five findings are ten lines, because each carries its
// fix, and ten lines is about where a reader stops reading and starts
// scrolling.
const SummaryThreshold = 5

// rollUp returns the rules whose findings are replaced by a count, and how
// many findings each has.
//
// Two conditions, and neither of them is the rule's namespace. That is the
// decision this function exists to make, and making it by severity rather
// than by who wrote the rule is what keeps it honest in both directions: a
// stele rule a repository has lowered to warning while it works through two
// hundred findings is rolled up too, and an aip rule a repository has raised
// to error prints in full — which is exactly what raising it asked for.
//
//   - Every finding of the rule is a warning. An error is the build failing
//     now, and a failure that will not say what it was is not one anybody can
//     act on. A warning is information a reader may act on later, and a count
//     is enough to decide with.
//   - There are more of them than SummaryThreshold, and the reader did not ask
//     for the rule by name.
//
// A rule's baselined findings are rolled up separately from the ones that
// still cost something, and on looser terms: they cannot fail the build, so a
// baselined *error* is rolled up where a live one never is. Counting the two
// together would be the failure this whole mechanism exists to prevent — one
// new finding hidden inside a count of forty old ones.
func (r *Report) rollUp() (live, held map[string]int) {
	detail := make(map[string]bool, len(r.Detail))
	for _, id := range r.Detail {
		detail[id] = true
	}
	live, held = make(map[string]int), make(map[string]int)
	errored := make(map[string]bool)
	for _, f := range r.Findings {
		if f.Baselined {
			held[f.Rule]++
			continue
		}
		live[f.Rule]++
		if f.Severity != SeverityWarning {
			errored[f.Rule] = true
		}
	}
	for id, n := range live {
		if errored[id] || detail[id] || n <= SummaryThreshold {
			delete(live, id)
		}
	}
	for id, n := range held {
		if detail[id] || n <= SummaryThreshold {
			delete(held, id)
		}
	}
	return live, held
}

// rollUpLine is what one rolled-up rule prints instead of its findings.
//
// It carries three things and each is there for a reason a summary usually
// misses: the count, so that the size of the thing is known without fetching
// it; the number of files, so that a hundred findings in one file reads
// differently from a hundred across thirty; and the exact command that prints
// them, so that the detail is a paste away rather than a flag somebody has to
// go and look up.
func (r *Report) rollUpLine(id string, baselined bool) string {
	n := 0
	files := make(map[string]bool)
	for _, f := range r.Findings {
		if f.Rule == id && f.Baselined == baselined {
			n++
			files[f.Path] = true
		}
	}
	unit := "warning"
	if baselined {
		// Not "warning": a baselined error is not a warning, it is a build
		// failure this repository is deliberately holding open, and calling it
		// one would lose the only thing that distinguishes them.
		unit = "baselined finding"
	}
	return fmt.Sprintf("stele: %s: %s in %s; see `stele lint --rule %s`",
		id, plural(n, unit), plural(len(files), "file"), id)
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Summary is one line saying what was checked and what was found. A clean run
// prints it too: a run that checked nothing looks exactly like a clean one
// without it.
func (r *Report) Summary() string {
	s := fmt.Sprintf("stele: lint checked %s with %s: %s, %s",
		plural(r.Files, "file"), plural(r.Rules, "rule"),
		plural(r.Errors, "error"), plural(r.Warnings, "warning"))
	// What the baseline is holding goes on the line that survives a truncated
	// log. A green run that is green because a file says so is a different
	// fact from a green run that is green because the contracts are clean,
	// and a summary that did not tell them apart would be the roll-up's
	// failure again: a comfortable line with an unread count behind it.
	if r.Baselined > 0 {
		s += fmt.Sprintf(", %s held by %s", plural(r.Baselined, "finding"), BaselineName)
	}
	if n := len(r.Stale); n > 0 {
		s += fmt.Sprintf(", %s of it stale", entries(n))
	}
	if n := len(r.Failures); n > 0 {
		s += fmt.Sprintf(", and %s did not run", plural(n, "rule check"))
	}
	if n := len(r.Notes); n > 0 {
		// The summary is the line that survives a truncated log, so the fact
		// that some of the judgement came from a binary nothing pins has to be
		// on it and not only in the note above.
		s += fmt.Sprintf(", %s not pinned", plural(n, "rule plugin"))
	}
	return s + "\n"
}

// entries is plural's exception: "entrys" is not a word, and the one place
// this format needs an irregular plural does not justify an inflection engine.
func entries(n int) string {
	if n == 1 {
		return "1 entry"
	}
	return fmt.Sprintf("%d entries", n)
}

func plural(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// Run lints the proto files this repository owns.
//
// Dependencies are resolved and compiled — they have to be, or an import would
// not link — and they are not judged. A lint that reported on somebody else's
// contract would redden a build over a file nobody here can change, and the
// only recovery available would be to switch the whole thing off. That is the
// failure mode this milestone exists to avoid, so it is a property of the
// command rather than a default somebody can get wrong.
func Run(ctx context.Context, opts Options) (*Report, error) {
	dir := opts.Dir
	if dir == "" {
		dir = "."
	}
	cfg, err := config.Load(filepath.Join(dir, ManifestName))
	if err != nil {
		return nil, err
	}
	rules := opts.Rules
	if rules == nil {
		rules = Builtin()
	}
	// The rule plugins are loaded before anything is fetched or compiled. Two
	// reasons, and both are about when an error is known: a plugin that will
	// not start has to be reported before a network round trip rather than
	// after one, and the rules it serves have to exist before the engine can
	// say whether a configured rule id names anything.
	set, unpinned, err := loadPlugins(ctx, cfg, dir, opts.CacheRoot)
	if err != nil {
		return nil, err
	}
	// The processes live exactly as long as the run that needs them. A rule
	// left running after the tool exits would be a process per lint.
	defer set.Close()
	rules = append(append([]Rule(nil), rules...), set.Rules()...)

	// The engine is built before anything is fetched or compiled: a rule id
	// nothing carries is a defect in the manifest, and reporting it after a
	// network round trip would be reporting it later than it is known.
	ecfg := ConfigFrom(cfg.Lint)
	ecfg.Only = opts.Only
	baselinePath := filepath.Join(dir, BaselineName)
	// A run taking a baseline is not judged against the one on disk: what it
	// records is what is there, and reading the old file first would carry
	// its stale entries into the new one.
	if !opts.UpdateBaseline {
		base, err := LoadBaseline(baselinePath)
		switch {
		case err == nil:
			ecfg.Baseline = base
		case !errors.Is(err, os.ErrNotExist):
			// A baseline that cannot be read is not a baseline that holds
			// nothing. Carrying on without it would redden a build for a
			// reason the output would blame on the contracts.
			return nil, err
		}
	}
	engine, err := New(rules, ecfg)
	if err != nil {
		return nil, err
	}

	fetch := opts.Fetch
	if fetch == nil {
		if len(cfg.Deps) > 0 && opts.CacheRoot == "" {
			return nil, errors.New("lint: the manifest declares dependencies, and this run was given no cache root")
		}
		fetch = networkFetch(opts.CacheRoot)
	}
	graph, err := pin.Resolve(ctx, pin.Options{
		Dir:      dir,
		Manifest: cfg,
		LockPath: filepath.Join(dir, LockName),
		Fetch:    fetch,
		Update:   opts.Update,
		NoLock:   opts.NoLock,
	})
	if err != nil {
		return nil, err
	}
	if opts.Warn != nil {
		resolve.WriteDrift(opts.Warn, graph.Drift())
	}

	subjects, disk := owned(graph, dir)
	if len(subjects) == 0 {
		return nil, errors.New("lint: this repository owns no proto files; there is nothing to lint")
	}
	compiled, err := compile.Compile(ctx, graph, subjects)
	if err != nil {
		return nil, err
	}
	files := make([]protoreflect.FileDescriptor, 0, len(compiled))
	for _, f := range compiled {
		files = append(files, f)
	}
	rep := &Report{Result: engine.Check(files), disk: disk, Notes: unpinnedNotes(unpinned), Detail: opts.Only}
	if opts.UpdateBaseline {
		if err := SaveBaseline(baselinePath, BaselineFrom(rep.Result)); err != nil {
			return nil, err
		}
		rep.Updated = true
	}
	// A run that checked nothing is not a clean run. The two ways to reach
	// here are an ignore list that grew until it covered everything and a
	// module path that no longer holds any protos, and both are silent: the
	// build stays green and the protection has gone.
	if rep.Files == 0 {
		return nil, errors.New("lint: no files were checked; every file this repository owns is covered by lint.ignore")
	}
	// A run narrowed to a rule the manifest switches off would print nothing
	// and look exactly like a rule with nothing to say. The reader asked to
	// see this rule's findings; the answer is that there are none to be had
	// until the manifest changes, and that is a sentence rather than silence.
	if len(opts.Only) > 0 && rep.Rules == 0 {
		return nil, fmt.Errorf("lint: %s is off in %s, so it has checked nothing; "+
			"a run narrowed to it has nothing to report",
			strings.Join(opts.Only, ", "), ManifestName)
	}
	return rep, nil
}

// Loaded is one rule a run would apply, and where it came from.
type Loaded struct {
	// Rule is the rule itself. A hosted rule is a rule.Rule like any other.
	Rule
	// Plugin is the manifest's name for the plugin serving it, or the empty
	// string for a rule that ships with this tool.
	Plugin string
	// Unpinned says why nothing pins the binary serving this rule, and is
	// empty for a rule that is pinned or built in. A listing that showed an
	// unpinned rule the same way it shows a pinned one would be a listing that
	// omitted the only thing about it a reader has to act on.
	Unpinned string
}

// Rules returns every rule a lint run in dir would apply: the ones this build
// carries, and the ones the manifest's plugins serve.
//
// It exists because a rule ID is a public contract that goes into lint.rules,
// and the IDs an external plugin serves are exactly the ones nobody can read
// out of this repository's source. A listing that showed only the built-ins
// would send somebody configuring a third-party rule to guess its ID.
//
// The returned function stops the plugin processes; it is never nil. A
// directory with no manifest yields the built-ins and no error: asking what
// rules a binary carries is a fair question to ask outside a repository.
func Rules(ctx context.Context, dir, cacheRoot string) ([]Loaded, func(), error) {
	if dir == "" {
		dir = "."
	}
	out := make([]Loaded, 0, len(Builtin()))
	for _, r := range Builtin() {
		out = append(out, Loaded{Rule: r})
	}
	cfg, err := config.Load(filepath.Join(dir, ManifestName))
	if errors.Is(err, os.ErrNotExist) {
		return out, func() {}, nil
	}
	if err != nil {
		return nil, func() {}, err
	}
	set, unpinned, err := loadPlugins(ctx, cfg, dir, cacheRoot)
	if err != nil {
		return nil, func() {}, err
	}
	for _, r := range set.Rules() {
		name := set.PluginFor(r.ID())
		out = append(out, Loaded{Rule: r, Plugin: name, Unpinned: unpinned[name]})
	}
	return out, func() { set.Close() }, nil
}

// loadPlugins resolves and starts the rule plugins the manifest declares.
//
// Where each binary comes from is decided by internal/plugin, which is the
// same code that decides it for a code generation plugin, so a rule plugin is
// installed, downloaded and verified exactly as a generator is. What this adds
// is the reason the answer matters here: a rule the tool cannot pin is a rule
// that can change what it says about a repository between two runs of an
// unchanged manifest.
//
// A plugin that cannot be resolved stops the run. There is no partial loading:
// a run missing a rule has not checked what that rule checks, and reporting
// the remaining rules' silence as a clean repository is the failure this whole
// tool exists to remove.
// The second return value maps a plugin name to why nothing pins it, for the
// plugins that declared `unpinned: true`. The manifest has already refused the
// tier without that opt-in; what is left is to make sure the opt-in is not
// something a reader has to go back to the manifest to discover.
func loadPlugins(ctx context.Context, cfg *config.File, dir, cacheRoot string) (*host.Set, map[string]string, error) {
	if cfg.Lint == nil || len(cfg.Lint.Plugins) == 0 {
		return nil, nil, nil
	}
	cache := plugin.Cache{Root: cacheRoot}
	plugins := make([]host.Plugin, 0, len(cfg.Lint.Plugins))
	var unpinned map[string]string
	for _, p := range cfg.Lint.Plugins {
		if cache.Root == "" && (p.Module != "" || len(p.Downloads) > 0) {
			tier := "downloads, which this tool fetches into its own cache"
			if p.Module != "" {
				tier = fmt.Sprintf("%s@%s, which this tool installs into its own cache", p.Module, p.Version)
			}
			return nil, nil, fmt.Errorf("lint plugin %q: it declares %s, but this run was given no cache root", p.Name, tier)
		}
		bin, err := cache.Resolve(ctx, plugin.Spec{
			Name: p.Name, Module: p.Module, Version: p.Version,
			Downloads: pluginDownloads(p.Downloads), Path: p.Path, Dir: dir,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("lint: %w", err)
		}
		if bin.Origin == plugin.OriginPath {
			if unpinned == nil {
				unpinned = make(map[string]string, 1)
			}
			unpinned[p.Name] = fmt.Sprintf("not pinned: it is whatever %s resolves to on PATH, which today is %s",
				p.Name, bin.Path)
		}
		plugins = append(plugins, host.Plugin{Name: bin.Name, Path: bin.Path})
	}
	set, err := host.Load(ctx, plugins)
	if err != nil {
		return nil, nil, err
	}
	return set, unpinned, nil
}

// unpinnedNotes turns the unpinned plugins into the lines a report opens with,
// in a stable order: a report whose bytes move between runs is not diffable.
func unpinnedNotes(unpinned map[string]string) []string {
	if len(unpinned) == 0 {
		return nil
	}
	names := make([]string, 0, len(unpinned))
	for name := range unpinned {
		names = append(names, name)
	}
	sort.Strings(names)
	notes := make([]string, 0, len(names))
	for _, name := range names {
		notes = append(notes, fmt.Sprintf("stele: lint: the rule plugin %q is %s. "+
			"What it says about this repository can change with nothing in the repository changing",
			name, unpinned[name]))
	}
	return notes
}

// pluginDownloads translates the manifest's download entries into the
// resolver's, as generation does. The two types stay separate so that the
// resolver does not depend on the manifest parser.
func pluginDownloads(ds []config.Download) []plugin.Download { return config.PluginDownloads(ds) }

// owned returns the import paths this repository supplies, and where each was
// read from on disk.
//
// A file is this repository's when the graph resolved it from no dependency.
// The origin is what decides, not the directory: a fetched tree that happened
// to land on the same path as a local module must not be able to pass its
// files off as this repository's.
func owned(g *resolve.Graph, dir string) ([]string, map[string]string) {
	var paths []string
	disk := make(map[string]string)
	for _, p := range g.ImportPaths() {
		f, ok := g.FileFor(p)
		if !ok || f.Origin.Git != "" {
			continue
		}
		paths = append(paths, p)
		if rel, err := filepath.Rel(dir, f.Path); err == nil && !strings.HasPrefix(rel, "..") {
			disk[p] = filepath.ToSlash(rel)
		} else {
			disk[p] = filepath.ToSlash(f.Path)
		}
	}
	sort.Strings(paths)
	return paths, disk
}

// networkFetch fetches repositories into a cache root. It is the same
// resolution generate and export use; nothing about linting changes where a
// dependency comes from.
func networkFetch(cacheRoot string) resolve.FetchFunc {
	hosts := source.DefaultHosts()
	return func(ctx context.Context, git, ref string) (string, string, error) {
		addr, err := source.ParseAddr(git, hosts)
		if err != nil {
			return "", "", err
		}
		return source.FetchInto(ctx, cacheRoot, addr.CloneURL(), ref)
	}
}
