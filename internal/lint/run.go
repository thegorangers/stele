package lint

import (
	"context"
	"errors"
	"fmt"
	"io"
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
func (r *Report) Failed() bool { return r.Errors > 0 || len(r.Failures) > 0 }

// Write renders every finding, in order, one to a line with its fix indented
// beneath.
//
// The path is the file on disk, relative to the directory the run was pointed
// at. The import path is what the rules see and what the ignore list matches,
// but it is not a file anybody can open: a dependency's `example/v1/a.proto`
// and this repository's are the same import path and different files, and a
// reader in a CI log has to know which one to go to.
func (r *Report) Write(w io.Writer) {
	// Failures first. They are the reason the rest of the output is
	// incomplete, and a reader who sees the findings first reads them as the
	// whole answer.
	for _, f := range r.Failures {
		if p, ok := r.disk[f.Path]; ok {
			f.Path = p
		}
		fmt.Fprintln(w, f.String())
	}
	for _, f := range r.Findings {
		if p, ok := r.disk[f.Path]; ok {
			f.Path = p
		}
		fmt.Fprintln(w, f.String())
	}
}

// Summary is one line saying what was checked and what was found. A clean run
// prints it too: a run that checked nothing looks exactly like a clean one
// without it.
func (r *Report) Summary() string {
	s := fmt.Sprintf("stele: lint checked %s with %s: %s, %s",
		plural(r.Files, "file"), plural(r.Rules, "rule"),
		plural(r.Errors, "error"), plural(r.Warnings, "warning"))
	if n := len(r.Failures); n > 0 {
		s += fmt.Sprintf(", and %s did not run", plural(n, "rule check"))
	}
	return s + "\n"
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
	set, err := loadPlugins(ctx, cfg, dir, opts.CacheRoot)
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
	engine, err := New(rules, ConfigFrom(cfg.Lint))
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
	rep := &Report{Result: engine.Check(files), disk: disk}
	// A run that checked nothing is not a clean run. The two ways to reach
	// here are an ignore list that grew until it covered everything and a
	// module path that no longer holds any protos, and both are silent: the
	// build stays green and the protection has gone.
	if rep.Files == 0 {
		return nil, errors.New("lint: no files were checked; every file this repository owns is covered by lint.ignore")
	}
	return rep, nil
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
func loadPlugins(ctx context.Context, cfg *config.File, dir, cacheRoot string) (*host.Set, error) {
	if cfg.Lint == nil || len(cfg.Lint.Plugins) == 0 {
		return nil, nil
	}
	cache := plugin.Cache{Root: cacheRoot}
	plugins := make([]host.Plugin, 0, len(cfg.Lint.Plugins))
	for _, p := range cfg.Lint.Plugins {
		if cache.Root == "" && (p.Module != "" || len(p.Downloads) > 0) {
			tier := "downloads, which this tool fetches into its own cache"
			if p.Module != "" {
				tier = fmt.Sprintf("%s@%s, which this tool installs into its own cache", p.Module, p.Version)
			}
			return nil, fmt.Errorf("lint plugin %q: it declares %s, but this run was given no cache root", p.Name, tier)
		}
		bin, err := cache.Resolve(ctx, plugin.Spec{
			Name: p.Name, Module: p.Module, Version: p.Version,
			Downloads: pluginDownloads(p.Downloads), Path: p.Path, Dir: dir,
		})
		if err != nil {
			return nil, fmt.Errorf("lint: %w", err)
		}
		plugins = append(plugins, host.Plugin{Name: bin.Name, Path: bin.Path})
	}
	return host.Load(ctx, plugins)
}

// pluginDownloads translates the manifest's download entries into the
// resolver's, as generation does. The two types stay separate so that the
// resolver does not depend on the manifest parser.
func pluginDownloads(ds []config.Download) []plugin.Download {
	if len(ds) == 0 {
		return nil
	}
	out := make([]plugin.Download, 0, len(ds))
	for _, d := range ds {
		out = append(out, plugin.Download{
			OS: d.OS, Arch: d.Arch, URL: d.URL, SHA256: d.SHA256, ArchivePath: d.ArchivePath,
		})
	}
	return out
}

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
