// Package gen runs code generation plugins over the resolved closure.
//
// Two decisions in here are about output bytes rather than about structure,
// and both are easy to get wrong in a way no test of the happy path would
// notice.
//
// The first is how the files being generated are split across requests. An
// input does not become one request: it becomes one request per directory of
// the files it selected, and a target with two inputs never merges them. That
// is measured, not chosen — see byDirectory — and it is visible in the output:
// a plugin that asks whether a message it references is also being generated
// answers differently depending on what else is in file_to_generate.
//
// The second is that resolution goes through pin.Resolve, exactly as export
// does, so that a generated tree and an exported tree can never be built from
// different commits of the same dependency.
package gen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bufbuild/protocompile/linker"
	"github.com/thegorangers/stele/internal/compile"
	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/genreq"
	"github.com/thegorangers/stele/internal/lockfile"
	"github.com/thegorangers/stele/internal/managed"
	"github.com/thegorangers/stele/internal/pin"
	"github.com/thegorangers/stele/internal/plugin"
	"github.com/thegorangers/stele/internal/report"
	"github.com/thegorangers/stele/internal/resolve"
	"github.com/thegorangers/stele/internal/source"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

// ManifestName is the manifest a generation is driven by.
const ManifestName = "stele.yaml"

// LockName is the file the resolved closure is pinned in.
const LockName = "stele.lock"

// Options configures one generation.
type Options struct {
	// Dir is the module root: the directory holding stele.yaml, and the
	// directory every path in that manifest — module roots and plugin output
	// directories alike — is relative to. Empty means ".".
	Dir string
	// Targets names the generate targets to run. Empty runs every target the
	// manifest declares. A name no target carries is an error, never a run of
	// nothing.
	Targets []string
	// IncludeImports puts the imports of the selected files into
	// file_to_generate, so the plugin emits code for them too.
	IncludeImports bool
	// Update re-resolves every ref to a fresh commit and rewrites the lock.
	Update bool
	// NoLock leaves the lock out of the run entirely.
	NoLock bool
	// Warn, when set, receives non-fatal findings from resolution — today,
	// the drift between a contract's owner and a stale vendored copy of it.
	// A finding that blocks nothing has to be visible somewhere, or the
	// staleness it reports goes on accumulating unseen.
	Warn io.Writer
	// Fetch materialises dependency repositories. When nil, repositories are
	// fetched from the network into CacheRoot.
	Fetch resolve.FetchFunc
	// CacheRoot is where fetched repositories are kept. It is required only
	// when Fetch is nil.
	CacheRoot string
}

// Run generates code for the manifest at Options.Dir and reports the versions
// that determined the output.
//
// The report is built from the plugins actually invoked rather than from the
// manifest, so that it is evidence about this run and not about a run that
// could have happened: a target that was not selected contributes nothing.
func Run(ctx context.Context, opts Options) (*report.Report, error) {
	dir := opts.Dir
	if dir == "" {
		dir = "."
	}

	cfg, err := config.Load(filepath.Join(dir, ManifestName))
	if err != nil {
		return nil, err
	}
	targets, err := selectTargets(cfg, opts.Targets)
	if err != nil {
		return nil, err
	}

	// Plugins are resolved before anything is fetched or compiled. A run that
	// discovered a missing or uninstallable plugin only after producing half
	// its output would leave the tree in a state nobody asked for, and the
	// install is the step most likely to need the network.
	binaries, err := resolvePlugins(ctx, dir, targets, plugin.Cache{Root: opts.CacheRoot}, opts.Warn)
	if err != nil {
		return nil, err
	}

	fetch := opts.Fetch
	if fetch == nil {
		if opts.CacheRoot == "" {
			return nil, errors.New("generate: no cache root and no fetcher")
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
		Plugins:  lockedPlugins(binaries),
		// A run of every target states the whole plugin set; a run of some
		// targets knows about some of it only.
		PluginsAuthoritative: len(opts.Targets) == 0,
	})
	if err != nil {
		return nil, err
	}

	if opts.Warn != nil {
		resolve.WriteDrift(opts.Warn, graph.Drift())
	}

	written := 0
	var invoked []report.Plugin
	for _, t := range targets {
		var mcfg *managed.Config
		if t.Managed != nil {
			c := t.Managed.Config()
			mcfg = &c
		}
		for i, in := range t.Inputs {
			files, err := selectFiles(graph, cfg, dir, in)
			if err != nil {
				return nil, fmt.Errorf("target %q, input %d: %w", t.Name, i, err)
			}
			compiled, err := compile.Compile(ctx, graph, files)
			if err != nil {
				return nil, fmt.Errorf("target %q, input %d: %w", t.Name, i, err)
			}
			for _, group := range byDirectory(compiled) {
				for _, p := range t.Plugins {
					req, err := genreq.Build(group, genreq.Target{
						Parameter: strings.Join(p.Opt, ","),
						Managed:   mcfg,
					})
					if err != nil {
						return nil, fmt.Errorf("target %q, input %d: %w", t.Name, i, err)
					}
					if opts.IncludeImports {
						includeImports(req)
					}
					bin := binaries[pluginKey(p)]
					invoked = append(invoked, report.Plugin{
						Name: bin.Name, Path: bin.Path, Module: bin.Module,
						Version: bin.Version, Origin: bin.Origin,
						URL: bin.URL, SHA256: bin.SHA256,
						OS: bin.OS, Arch: bin.Arch,
					})
					resp, err := plugin.Run(ctx, bin.Path, req)
					if err != nil {
						return nil, fmt.Errorf("target %q, input %d: %w", t.Name, i, err)
					}
					if err := check(p.Local, req, resp); err != nil {
						return nil, fmt.Errorf("target %q, input %d: %w", t.Name, i, err)
					}
					n, err := writeResponse(filepath.Join(dir, filepath.FromSlash(p.Out)), p.Local, resp)
					if err != nil {
						return nil, fmt.Errorf("target %q, input %d: %w", t.Name, i, err)
					}
					written += n
				}
			}
		}
	}
	// The rule the design states about the command as a whole: a run that
	// produced nothing has been asked the wrong question, and reporting
	// success would leave the author to discover that from an empty tree.
	if written == 0 {
		return nil, errors.New("generate: the plugins wrote no files; a run that generates nothing is an error, not an empty success")
	}
	return report.Build(invoked), nil
}

// pluginKey identifies a manifest plugin by everything that decides which
// binary it is. Two targets naming the same plugin at the same version are one
// binary; the same name at two versions is two, and must not collapse.
func pluginKey(p config.Plugin) string {
	parts := []string{p.Local, p.Module, p.Version, p.Path}
	for _, d := range p.Downloads {
		parts = append(parts, d.OS, d.Arch, d.URL, d.SHA256, d.ArchivePath)
	}
	return strings.Join(parts, "\x00")
}

// resolvePlugins turns every plugin of the selected targets into a runnable
// binary, installing the declared ones.
//
// A declared plugin needs a cache to be installed into. Rather than let the
// installer fail with a message about an empty root, the absence is reported
// against the plugin that needed it, because that is the fact the reader has
// to act on.
func resolvePlugins(ctx context.Context, dir string, targets []config.GenTarget, cache plugin.Cache, warn io.Writer) (map[string]plugin.Binary, error) {
	out := make(map[string]plugin.Binary)
	for _, t := range targets {
		for _, p := range t.Plugins {
			k := pluginKey(p)
			if _, done := out[k]; done {
				continue
			}
			if p.Module != "" && cache.Root == "" {
				return nil, fmt.Errorf("target %q, plugin %q: it declares %s@%s, which this tool installs into its own cache, "+
					"but this run was given no cache root", t.Name, p.Local, p.Module, p.Version)
			}
			if len(p.Downloads) > 0 && cache.Root == "" {
				return nil, fmt.Errorf("target %q, plugin %q: it declares downloads, which this tool fetches into its own cache, "+
					"but this run was given no cache root", t.Name, p.Local)
			}
			bin, err := cache.Resolve(ctx, plugin.Spec{
				Name: p.Local, Module: p.Module, Version: p.Version,
				Downloads: downloads(p), Path: p.Path, Dir: dir,
			})
			if err != nil {
				return nil, fmt.Errorf("target %q: %w", t.Name, err)
			}
			if bin.Warning != "" && warn != nil {
				fmt.Fprintln(warn, "stele: "+bin.Warning)
			}
			out[k] = bin
		}
	}
	return out, nil
}

// downloads translates the manifest's download entries into the resolver's.
// The two types are separate so that the resolver does not depend on the
// manifest parser, which is what lets a plugin be resolved from anywhere.
func downloads(p config.Plugin) []plugin.Download {
	if len(p.Downloads) == 0 {
		return nil
	}
	out := make([]plugin.Download, 0, len(p.Downloads))
	for _, d := range p.Downloads {
		out = append(out, plugin.Download{
			OS: d.OS, Arch: d.Arch, URL: d.URL, SHA256: d.SHA256, ArchivePath: d.ArchivePath,
		})
	}
	return out
}

// lockedPlugins renders the resolved binaries as lock entries, sorted by name
// so that the same run always writes the same bytes.
func lockedPlugins(binaries map[string]plugin.Binary) []lockfile.Plugin {
	out := make([]lockfile.Plugin, 0, len(binaries))
	for _, b := range binaries {
		out = append(out, lockfile.Plugin{
			Name: b.Name, Origin: b.Origin, Module: b.Module, Version: b.Version,
			URL: b.URL, SHA256: b.SHA256, ArchivePath: b.ArchivePath,
			OS: b.OS, Arch: b.Arch, Path: b.Declared,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// selectTargets returns the targets to run, in manifest order.
func selectTargets(cfg *config.File, names []string) ([]config.GenTarget, error) {
	if len(cfg.Generate) == 0 {
		return nil, errors.New("generate: the manifest declares no generate targets")
	}
	if len(names) == 0 {
		return cfg.Generate, nil
	}
	declared := make([]string, 0, len(cfg.Generate))
	for _, t := range cfg.Generate {
		declared = append(declared, t.Name)
	}
	var out []config.GenTarget
	for _, want := range names {
		found := false
		for _, t := range cfg.Generate {
			if t.Name == want {
				out = append(out, t)
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("generate: no target named %q; the manifest declares: %s",
				want, strings.Join(declared, ", "))
		}
	}
	return out, nil
}

// selectFiles returns the import paths one input selects.
//
// A module input takes only files this manifest owns: a dependency's files
// reach a plugin as imports of what was selected, if at all. A dep input takes
// only the files of the module that dependency's entry named — the producer's
// other roots are in the graph so that imports resolve, not because this
// manifest asked to generate from them.
func selectFiles(g *resolve.Graph, cfg *config.File, dir string, in config.Input) ([]string, error) {
	root, subject, err := inputRoot(g, cfg, dir, in)
	if err != nil {
		return nil, err
	}
	// The origin is checked as well as the root, so that a fetched tree that
	// happened to land on the same directory as this manifest's own could not
	// pass files off as the wrong side's.
	local := in.Dep == ""
	var own []string
	for _, p := range g.ImportPaths() {
		f, ok := g.FileFor(p)
		if ok && (f.Origin.Git == "") == local && filepath.Clean(f.Root) == root {
			own = append(own, p)
		}
	}
	if len(own) == 0 {
		return nil, fmt.Errorf("%s contains no proto files", subject)
	}
	if len(in.Paths) == 0 {
		return own, nil
	}

	matched := make(map[string]bool, len(in.Paths))
	keep := make(map[string]bool, len(own))
	for _, raw := range in.Paths {
		want := path.Clean(filepath.ToSlash(strings.TrimSpace(raw)))
		for _, p := range own {
			if p == want || strings.HasPrefix(p, want+"/") {
				matched[raw] = true
				keep[p] = true
			}
		}
	}
	var missed []string
	for _, raw := range in.Paths {
		if !matched[raw] {
			missed = append(missed, raw)
		}
	}
	if len(missed) > 0 {
		// The commonest cause is a coordinate written relative to the
		// workspace, or to the producer's repository, instead of the module
		// root, and listing what the module does supply says so at a glance.
		return nil, fmt.Errorf("paths matched no files: %s; %s supplies: %s",
			strings.Join(missed, ", "), subject, strings.Join(own, ", "))
	}
	out := make([]string, 0, len(keep))
	for p := range keep {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// inputRoot returns the directory an input's files must live under, and the
// phrase naming it in an error.
//
// The dependency case deliberately resolves nothing of its own: it looks the
// dependency up in the graph the lock already pinned. A second way to reach a
// producer's tree is a second way for a generated tree and an exported tree to
// come from different commits.
func inputRoot(g *resolve.Graph, cfg *config.File, dir string, in config.Input) (string, string, error) {
	if in.Dep == "" {
		root := filepath.Clean(filepath.Join(dir, filepath.FromSlash(in.Module)))
		return root, fmt.Sprintf("module %q", in.Module), nil
	}
	var d config.Dep
	for _, cand := range cfg.Deps {
		if cand.Name == in.Dep {
			d = cand
			break
		}
	}
	if d.Name == "" {
		// config validation rejects this; the check is here so that a caller
		// constructing a File in code cannot reach a nil dependency silently.
		return "", "", fmt.Errorf("dependency %q is not declared in deps", in.Dep)
	}
	for _, o := range g.Deps() {
		if o.Git != d.Git || o.Ref != d.Ref {
			continue
		}
		root, err := resolve.ModuleRoot(o.Dir, d.Module)
		if err != nil {
			return "", "", err
		}
		return root, fmt.Sprintf("dependency %q, module %q", d.Name, moduleName(d.Module)), nil
	}
	return "", "", fmt.Errorf("dependency %q was not resolved", in.Dep)
}

// moduleName renders a dep's module for a message, spelling the repository
// root the way the manifest may omit it.
func moduleName(m string) string {
	if m == "" {
		return "."
	}
	return m
}

// byDirectory splits the files being generated into one group per directory,
// in directory order, each group holding that directory's files in the order
// they were compiled.
//
// This mirrors the tool being replaced, whose default is exactly this split,
// and it was found by measurement rather than by reading: two repositories
// generate byte-identical code either way, and the third does not. A plugin
// that emits a fast path for a message type only when that type is also being
// generated in the same request — vtprotobuf does — writes different code for
// a cross-directory reference depending on the split. Merging an input into a
// single request is not a simplification of this; it is a different output.
//
// The closure is not split: each request still carries the transitive imports
// of its own group, because genreq walks them from the descriptors.
func byDirectory(files linker.Files) []linker.Files {
	order := make([]string, 0, len(files))
	groups := make(map[string]linker.Files, len(files))
	for _, f := range files {
		d := path.Dir(f.Path())
		if _, ok := groups[d]; !ok {
			order = append(order, d)
		}
		groups[d] = append(groups[d], f)
	}
	sort.Strings(order)
	out := make([]linker.Files, 0, len(order))
	for _, d := range order {
		out = append(out, groups[d])
	}
	return out
}

// includeImports promotes the imports of the request's closure to targets.
//
// The order is the request's own proto_file order — topological, imports
// first — rather than the sorted order of the targets, because that is the
// order the file list already has and a second ordering rule would be one more
// thing that could disagree with the descriptors.
//
// The well-known types are left out. That is measured, not assumed: dumping
// the request the tool being replaced sends with imports included shows every
// other import promoted — google/api and google/type among them — and
// google/protobuf absent. It has to be absent, because the Go runtime already
// ships generated code for those files and a second copy under the consumer's
// own import path would be a different set of types with the same names.
func includeImports(req *pluginpb.CodeGeneratorRequest) {
	all := make([]string, 0, len(req.GetProtoFile()))
	for _, f := range req.GetProtoFile() {
		if genreq.IsWellKnown(f.GetName()) {
			continue
		}
		all = append(all, f.GetName())
	}
	req.FileToGenerate = all
}

// check refuses a response this tool cannot honour faithfully.
//
// Insertion points are deliberately NOT implemented: none of the plugins in
// the measured surface emits one, and an unimplemented feature that is
// silently dropped produces output that is wrong in a way no diff of this
// tool's own files would show. So a response that uses one is an error saying
// exactly that, and the day a consumer needs them the error is where the
// implementation goes.
//
// The proto3-optional feature is checked because it is the compiler's job to
// check it: a plugin that does not declare the feature will mis-handle such a
// field rather than complain, and the mistake surfaces as generated code that
// is quietly missing presence.
func check(bin string, req *pluginpb.CodeGeneratorRequest, resp *pluginpb.CodeGeneratorResponse) error {
	for _, f := range resp.GetFile() {
		if ip := f.GetInsertionPoint(); ip != "" {
			return fmt.Errorf("plugin %q asked to insert into %q at insertion point %q; "+
				"insertion points are not implemented", bin, f.GetName(), ip)
		}
	}
	features := resp.GetSupportedFeatures()
	if features&uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL) == 0 {
		if f, field := proto3Optional(req); f != "" {
			return fmt.Errorf("plugin %q does not support proto3 optional fields, and %s declares one (%s)",
				bin, f, field)
		}
	}
	return nil
}

// proto3Optional finds the first proto3 optional field among the files the
// plugin was asked to generate, returning the file and the field.
func proto3Optional(req *pluginpb.CodeGeneratorRequest) (string, string) {
	wanted := make(map[string]bool, len(req.GetFileToGenerate()))
	for _, n := range req.GetFileToGenerate() {
		wanted[n] = true
	}
	for _, f := range req.GetProtoFile() {
		if !wanted[f.GetName()] {
			continue
		}
		var walk func(prefix string, ms []*descriptorpb.DescriptorProto) string
		walk = func(prefix string, ms []*descriptorpb.DescriptorProto) string {
			for _, m := range ms {
				for _, field := range m.GetField() {
					if field.GetProto3Optional() {
						return prefix + m.GetName() + "." + field.GetName()
					}
				}
				if got := walk(prefix+m.GetName()+".", m.GetNestedType()); got != "" {
					return got
				}
			}
			return ""
		}
		if got := walk("", f.GetMessageType()); got != "" {
			return f.GetName(), got
		}
	}
	return "", ""
}

// writeResponse writes the plugin's files under out and reports how many it
// wrote.
func writeResponse(out, bin string, resp *pluginpb.CodeGeneratorResponse) (int, error) {
	for _, f := range resp.GetFile() {
		name := f.GetName()
		// A plugin names its output; nothing stops a broken one from naming a
		// path outside the directory it was given, and a tool that obeyed
		// would write wherever it was told.
		clean := path.Clean(filepath.ToSlash(name))
		if name == "" || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return 0, fmt.Errorf("plugin %q returned the file name %q, which is not a path inside %s", bin, name, out)
		}
		dst := filepath.Join(out, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return 0, err
		}
		if err := os.WriteFile(dst, []byte(f.GetContent()), 0o644); err != nil {
			return 0, err
		}
	}
	return len(resp.GetFile()), nil
}

// networkFetch fetches repositories into a cache root.
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
