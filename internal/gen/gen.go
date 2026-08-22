// Package gen runs code generation plugins over the resolved closure.
//
// Two decisions in here are about output bytes rather than about structure,
// and both are easy to get wrong in a way no test of the happy path would
// notice.
//
// The first is that each input of a target gets its own request. A target with
// two inputs is not a target with one input holding both: file_to_generate
// differs, and so does everything a plugin derives from it. Merging them would
// be simpler and would produce different code.
//
// The second is that resolution goes through pin.Resolve, exactly as export
// does, so that a generated tree and an exported tree can never be built from
// different commits of the same dependency.
package gen

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/thegorangers/stele/internal/compile"
	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/genreq"
	"github.com/thegorangers/stele/internal/managed"
	"github.com/thegorangers/stele/internal/pin"
	"github.com/thegorangers/stele/internal/plugin"
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
	// Fetch materialises dependency repositories. When nil, repositories are
	// fetched from the network into CacheRoot.
	Fetch resolve.FetchFunc
	// CacheRoot is where fetched repositories are kept. It is required only
	// when Fetch is nil.
	CacheRoot string
}

// Run generates code for the manifest at Options.Dir.
func Run(ctx context.Context, opts Options) error {
	dir := opts.Dir
	if dir == "" {
		dir = "."
	}

	cfg, err := config.Load(filepath.Join(dir, ManifestName))
	if err != nil {
		return err
	}
	targets, err := selectTargets(cfg, opts.Targets)
	if err != nil {
		return err
	}

	fetch := opts.Fetch
	if fetch == nil {
		if opts.CacheRoot == "" {
			return errors.New("generate: no cache root and no fetcher")
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
		return err
	}

	written := 0
	for _, t := range targets {
		var mcfg *managed.Config
		if t.Managed != nil {
			c := t.Managed.Config()
			mcfg = &c
		}
		for i, in := range t.Inputs {
			files, err := selectFiles(graph, cfg, dir, in)
			if err != nil {
				return fmt.Errorf("target %q, input %d: %w", t.Name, i, err)
			}
			compiled, err := compile.Compile(ctx, graph, files)
			if err != nil {
				return fmt.Errorf("target %q, input %d: %w", t.Name, i, err)
			}
			for _, p := range t.Plugins {
				req, err := genreq.Build(compiled, genreq.Target{
					Parameter: strings.Join(p.Opt, ","),
					Managed:   mcfg,
				})
				if err != nil {
					return fmt.Errorf("target %q, input %d: %w", t.Name, i, err)
				}
				if opts.IncludeImports {
					includeImports(req)
				}
				resp, err := plugin.Run(ctx, p.Local, req)
				if err != nil {
					return fmt.Errorf("target %q, input %d: %w", t.Name, i, err)
				}
				if err := check(p.Local, req, resp); err != nil {
					return fmt.Errorf("target %q, input %d: %w", t.Name, i, err)
				}
				n, err := writeResponse(filepath.Join(dir, filepath.FromSlash(p.Out)), p.Local, resp)
				if err != nil {
					return fmt.Errorf("target %q, input %d: %w", t.Name, i, err)
				}
				written += n
			}
		}
	}
	// The rule the design states about the command as a whole: a run that
	// produced nothing has been asked the wrong question, and reporting
	// success would leave the author to discover that from an empty tree.
	if written == 0 {
		return errors.New("generate: the plugins wrote no files; a run that generates nothing is an error, not an empty success")
	}
	return nil
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

// includeImports promotes every file of the request's closure to a target.
//
// The order is the request's own proto_file order — topological, imports
// first — rather than the sorted order of the targets, because that is the
// order the file list already has and a second ordering rule would be one more
// thing that could disagree with the descriptors.
func includeImports(req *pluginpb.CodeGeneratorRequest) {
	all := make([]string, 0, len(req.GetProtoFile()))
	for _, f := range req.GetProtoFile() {
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
