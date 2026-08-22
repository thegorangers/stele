// Package migrate translates a buf configuration into a stele manifest.
//
// The translation covers a measured subset and nothing more. Every shape
// outside that subset is refused by name, because the worst outcome here is
// not a refusal: it is a manifest that looks migrated and is not. A fleet-wide
// hand translation drifts from reality and nobody notices; a program's
// translation can be checked byte for byte against the tool it replaces.
package migrate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/thegorangers/stele/internal/config"
)

// Result is a translated manifest together with everything the translation
// could not carry over.
type Result struct {
	// File is the manifest. It is only worth writing when Complete reports
	// true; until then it is a draft with holes named in Unresolved.
	File *config.File
	// Unresolved names what a human must still decide. Each entry blocks
	// completion.
	Unresolved []string
	// Notes name what was dropped on purpose because this tool has no
	// counterpart for it. They do not block completion.
	Notes []string
}

// Complete reports whether the translation left nothing for a human to decide.
func (r *Result) Complete() bool { return len(r.Unresolved) == 0 }

// defaultTargetName is the name given to the single generate target a
// buf.gen.yaml describes. buf has no name for it; the manifest requires one.
const defaultTargetName = "default"

// FromDir translates the buf configuration of a working copy. buf.yaml and
// Makefile are optional — two of the measured repositories own no protos and
// have no buf.yaml at all — but buf.gen.yaml is not: without it there is no
// generation to describe.
func FromDir(dir string) (*Result, error) {
	optional := func(name string) ([]byte, error) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return b, err
	}
	bufYAMLRaw, err := optional("buf.yaml")
	if err != nil {
		return nil, err
	}
	makefile, err := optional("Makefile")
	if err != nil {
		return nil, err
	}
	genRaw, err := os.ReadFile(filepath.Join(dir, "buf.gen.yaml"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s: no buf.gen.yaml; there is nothing to migrate", dir)
		}
		return nil, err
	}
	return FromBuf(bufYAMLRaw, genRaw, makefile)
}

// FromBuf translates one set of buf files. Any of bufYAML and makefile may be
// nil; bufGenYAML may not.
func FromBuf(bufYAMLRaw, bufGenYAMLRaw, makefile []byte) (*Result, error) {
	m := &migration{}
	if err := m.readBufYAML(bufYAMLRaw); err != nil {
		return nil, err
	}
	if err := m.readBufGenYAML(bufGenYAMLRaw); err != nil {
		return nil, err
	}
	if err := m.readMakefile(makefile); err != nil {
		return nil, err
	}
	if err := m.build(); err != nil {
		return nil, err
	}
	return &m.result, nil
}

// migration carries the state of one translation.
type migration struct {
	buf     bufYAML
	gen     bufGenYAML
	exports []bufExport
	// vendored maps an export output directory to the dependencies filling
	// it. Such a directory is not a module of this repository: it is somebody
	// else's protos, copied in.
	vendored map[string][]string
	// deps is the translated dependency set, keyed by name, in insertion
	// order via depOrder.
	deps     map[string]*config.Dep
	depOrder []string
	// depPaths records the export --path arguments of a dependency, used to
	// decide which dependency a vendored input path belongs to.
	depPaths map[string][]string
	result   Result
}

func (m *migration) unresolved(format string, a ...any) {
	m.result.Unresolved = append(m.result.Unresolved, fmt.Sprintf(format, a...))
}

func (m *migration) note(format string, a ...any) {
	m.result.Notes = append(m.result.Notes, fmt.Sprintf(format, a...))
}

func (m *migration) readBufYAML(raw []byte) error {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	if err := parseStrict("buf.yaml", raw, &m.buf); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if m.buf.Version != bufVersion {
		return fmt.Errorf("buf.yaml: version %s is not supported; only %s is translated",
			orNone(m.buf.Version), bufVersion)
	}
	for i, mod := range m.buf.Modules {
		if mod.Path == "" {
			return fmt.Errorf("buf.yaml: modules[%d].path: missing", i)
		}
		if len(mod.Excludes) > 0 {
			return fmt.Errorf("buf.yaml: modules[%d].excludes: not supported; it changes which files reach the compiler and there is no counterpart to translate it into", i)
		}
		if mod.Name != "" {
			m.note("buf.yaml: modules[%d].name %s dropped: this tool has no schema registry", i, mod.Name)
		}
	}
	if m.buf.Lint || m.buf.Breaking {
		m.note("buf.yaml: lint and breaking configuration dropped: this tool implements neither check")
	}
	return nil
}

func (m *migration) readBufGenYAML(raw []byte) error {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return errors.New("buf.gen.yaml: empty; there is nothing to migrate")
	}
	if err := parseStrict("buf.gen.yaml", raw, &m.gen); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if m.gen.Version != bufVersion {
		return fmt.Errorf("buf.gen.yaml: version %s is not supported; only %s is translated",
			orNone(m.gen.Version), bufVersion)
	}
	if m.gen.Clean != nil {
		m.note("buf.gen.yaml: clean dropped: this tool does not remove output directories")
	}
	return nil
}

func (m *migration) readMakefile(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	exports, unparsed, err := parseExports(raw)
	if err != nil {
		return err
	}
	for _, u := range unparsed {
		m.unresolved("Makefile: could not translate an export invocation: %s", u)
	}
	m.exports = exports
	return nil
}

// build assembles the manifest from what was read.
func (m *migration) build() error {
	m.deps = map[string]*config.Dep{}
	m.depPaths = map[string][]string{}
	m.vendored = map[string][]string{}

	if err := m.buildDepsFromExports(); err != nil {
		return err
	}
	modules, err := m.buildModules()
	if err != nil {
		return err
	}
	target := config.GenTarget{Name: defaultTargetName}
	if target.Plugins, err = m.buildPlugins(); err != nil {
		return err
	}
	if target.Managed, err = m.buildManaged(); err != nil {
		return err
	}
	if target.Inputs, err = m.buildInputs(modules); err != nil {
		return err
	}
	f := &config.File{Version: config.Version, Modules: modules, Generate: []config.GenTarget{target}}
	for _, name := range m.depOrder {
		d := m.deps[name]
		if d.Ref == "" {
			m.unresolved("deps %s: buf export pins no commit, so none was carried over; pin one before running stele", name)
		}
		f.Deps = append(f.Deps, *d)
	}
	m.result.File = f
	return nil
}

// buildDepsFromExports turns the recovered export invocations into
// dependencies. A registry reference is reported rather than resolved: there
// is no git repository to name, and inventing one would be a guess.
func (m *migration) buildDepsFromExports() error {
	for _, e := range m.exports {
		m.vendored[e.Output] = m.vendored[e.Output]
		if e.Registry {
			m.unresolved("Makefile: %s is a schema registry reference and has no git address; declare the producing repository by hand (its files are vendored under %s)",
				e.Target, e.Output)
			continue
		}
		name := depName(e.Git)
		if name == "" {
			return fmt.Errorf("Makefile: cannot derive a dependency name from %s", e.Git)
		}
		d, err := m.addDep(name, e.Git, e.Ref, e.Subdir, e.Paths)
		if err != nil {
			return err
		}
		m.vendored[e.Output] = append(m.vendored[e.Output], d.Name)
		m.depPaths[d.Name] = append(m.depPaths[d.Name], e.Paths...)
	}
	return nil
}

// addDep records a dependency, merging a repeated one and refusing a
// contradictory one. Two entries for the same repository at different commits
// would put two versions of one import path in the graph.
func (m *migration) addDep(name, git, ref, module string, paths []string) (*config.Dep, error) {
	if d, ok := m.deps[name]; ok {
		switch {
		case d.Git != git:
			return nil, fmt.Errorf("two different repositories would both be called %q: %s and %s", name, d.Git, git)
		case d.Ref != ref:
			return nil, fmt.Errorf("dependency %q appears twice at different refs: %s and %s", name, orNone(d.Ref), orNone(ref))
		case d.Module != module:
			return nil, fmt.Errorf("dependency %q appears twice at different modules: %s and %s", name, orNone(d.Module), orNone(module))
		}
		d.Paths = appendUnique(d.Paths, paths...)
		return d, nil
	}
	d := &config.Dep{Name: name, Git: git, Ref: ref, Module: module, Paths: appendUnique(nil, paths...)}
	m.deps[name] = d
	m.depOrder = append(m.depOrder, name)
	return d, nil
}

// buildModules keeps the modules this repository owns and drops the vendored
// trees, which are dependencies now.
func (m *migration) buildModules() ([]config.Module, error) {
	var out []config.Module
	for _, mod := range m.buf.Modules {
		p := path.Clean(mod.Path)
		if _, ok := m.vendored[p]; ok {
			m.note("buf.yaml: module %s dropped: it is a vendored tree, and its contents are declared as dependencies instead", p)
			continue
		}
		out = append(out, config.Module{Path: p})
	}
	return out, nil
}

func (m *migration) buildPlugins() ([]config.Plugin, error) {
	if len(m.gen.Plugins) == 0 {
		return nil, errors.New("buf.gen.yaml: plugins: none declared; there is nothing to generate")
	}
	var out []config.Plugin
	for i, p := range m.gen.Plugins {
		switch {
		case p.Remote != "":
			return nil, fmt.Errorf("buf.gen.yaml: plugins[%d].remote: remote plugins are not supported; this tool never contacts a plugin registry (found %s)", i, p.Remote)
		case p.ProtocBuiltin != "":
			return nil, fmt.Errorf("buf.gen.yaml: plugins[%d].protoc_builtin: not supported; this tool runs standalone plugin binaries only (found %s)", i, p.ProtocBuiltin)
		case p.Strategy != "":
			return nil, fmt.Errorf("buf.gen.yaml: plugins[%d].strategy: not supported; %s changes how files are batched into requests and this tool has one batching rule", i, p.Strategy)
		case len(p.Local) == 0:
			return nil, fmt.Errorf("buf.gen.yaml: plugins[%d].local: missing", i)
		case len(p.Local) > 1:
			return nil, fmt.Errorf("buf.gen.yaml: plugins[%d].local: a plugin invoked with arguments (%s) is not supported; name an executable", i, strings.Join(p.Local, " "))
		case p.Out == "":
			return nil, fmt.Errorf("buf.gen.yaml: plugins[%d].out: missing for plugin %s", i, p.Local[0])
		case p.IncludeImports != nil && *p.IncludeImports:
			return nil, fmt.Errorf("buf.gen.yaml: plugins[%d].include_imports: not supported; it generates code for files this manifest does not select", i)
		case p.IncludeWKT != nil && *p.IncludeWKT:
			return nil, fmt.Errorf("buf.gen.yaml: plugins[%d].include_wkt: not supported; it generates code for the well-known types", i)
		}
		if p.Revision != nil {
			m.note("buf.gen.yaml: plugins[%d].revision dropped: it selects a registry plugin revision, and plugins here are local binaries", i)
		}
		out = append(out, config.Plugin{Local: p.Local[0], Out: path.Clean(p.Out), Opt: []string(p.Opt)})
	}
	return out, nil
}

func (m *migration) buildManaged() (*config.Managed, error) {
	g := m.gen.Managed
	if g == nil {
		return nil, nil
	}
	if len(g.Disable) > 0 {
		return nil, errors.New("buf.gen.yaml: managed.disable: not supported; it removes synthesised options case by case and this tool synthesises only what override names")
	}
	if g.Enabled == nil || !*g.Enabled {
		if len(g.Override) > 0 {
			return nil, errors.New("buf.gen.yaml: managed.override is set while managed.enabled is not true; buf synthesises nothing in that state and translating the overrides would change the generated bytes")
		}
		m.note("buf.gen.yaml: managed block dropped: it is not enabled and synthesises nothing")
		return nil, nil
	}
	if len(g.Override) == 0 {
		return nil, errors.New("buf.gen.yaml: managed.enabled is true with no override: buf's default managed options are not reproduced by this tool, so there is nothing faithful to translate")
	}
	out := &config.Managed{}
	for i, o := range g.Override {
		switch {
		case o.Module != "":
			return nil, fmt.Errorf("buf.gen.yaml: managed.override[%d].module: a module selector is not supported; only a path selector is (found %s)", i, o.Module)
		case o.Field != "":
			return nil, fmt.Errorf("buf.gen.yaml: managed.override[%d].field: field options are not supported (found %s)", i, o.Field)
		case o.FileOption != config.FileOptionGoPackagePrefix:
			return nil, fmt.Errorf("buf.gen.yaml: managed.override[%d].file_option: %s is not synthesised by this tool; only %s is",
				i, orNone(o.FileOption), config.FileOptionGoPackagePrefix)
		case o.Value == "":
			return nil, fmt.Errorf("buf.gen.yaml: managed.override[%d].value: missing", i)
		}
		out.Override = append(out.Override, config.Override{FileOption: o.FileOption, Path: o.Path, Value: o.Value})
	}
	return out, nil
}

// buildInputs translates the generate inputs. An absent inputs block is not
// left implicit: buf falls back to the whole workspace, and an implicit
// fallback in a migrated manifest is exactly the enumeration that drifts
// unnoticed.
func (m *migration) buildInputs(modules []config.Module) ([]config.Input, error) {
	if len(m.gen.Inputs) == 0 {
		out := m.wholeWorkspace(modules)
		if len(out) == 0 {
			return nil, errors.New("buf.gen.yaml: no inputs, and no modules to expand the default input into")
		}
		m.note("buf.gen.yaml: no inputs declared; buf would take the whole workspace, expanded here into one input per module")
		return out, nil
	}
	var out []config.Input
	for i, in := range m.gen.Inputs {
		switch {
		case len(in.Types) > 0:
			return nil, fmt.Errorf("buf.gen.yaml: inputs[%d].types: not supported; type filtering changes the descriptor set and this tool does not implement it", i)
		case len(in.ExcludePaths) > 0:
			return nil, fmt.Errorf("buf.gen.yaml: inputs[%d].exclude_paths: not supported; only inclusion by paths is", i)
		case in.Module != "":
			return nil, fmt.Errorf("buf.gen.yaml: inputs[%d].module: a registry module input is not supported; this tool takes protos from git (found %s)", i, in.Module)
		case in.ExcludeImports != nil && !*in.ExcludeImports:
			return nil, fmt.Errorf("buf.gen.yaml: inputs[%d].exclude_imports: false is not supported; imports are never generated for", i)
		case in.IncludeImports != nil && *in.IncludeImports:
			return nil, fmt.Errorf("buf.gen.yaml: inputs[%d].include_imports: not supported; it generates code for files this manifest does not select", i)
		case in.GitRepo != "":
			got, err := m.gitRepoInput(i, in)
			if err != nil {
				return nil, err
			}
			out = append(out, got...)
			continue
		case in.Directory == "":
			return nil, fmt.Errorf("buf.gen.yaml: inputs[%d]: neither directory nor git_repo", i)
		}
		got, err := m.directoryInput(i, in)
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	return out, nil
}

// wholeWorkspace expands buf's default input: every module of the workspace,
// vendored trees included, which are dependencies here.
func (m *migration) wholeWorkspace(modules []config.Module) []config.Input {
	var out []config.Input
	for _, mod := range modules {
		out = append(out, config.Input{Module: mod.Path})
	}
	for _, mod := range m.buf.Modules {
		for _, name := range m.vendored[path.Clean(mod.Path)] {
			out = append(out, config.Input{Dep: name})
		}
	}
	return out
}

func (m *migration) gitRepoInput(i int, in bufInput) ([]config.Input, error) {
	switch {
	case in.Branch != "":
		return nil, fmt.Errorf("buf.gen.yaml: inputs[%d].branch: a branch pins nothing; pin a commit", i)
	case in.Ref == "":
		return nil, fmt.Errorf("buf.gen.yaml: inputs[%d].ref: missing for %s; an unpinned input is not reproducible", i, in.GitRepo)
	case in.Depth != nil:
		return nil, fmt.Errorf("buf.gen.yaml: inputs[%d].depth: not supported; this tool decides its own fetch depth", i)
	}
	name := depName(in.GitRepo)
	if name == "" {
		return nil, fmt.Errorf("buf.gen.yaml: inputs[%d].git_repo: cannot derive a dependency name from %s", i, in.GitRepo)
	}
	// buf paths on a git input are relative to the workspace of the fetched
	// repository, so subdir is their prefix exactly as a directory is.
	paths, err := rebase(fmt.Sprintf("buf.gen.yaml: inputs[%d]", i), in.Subdir, in.Paths)
	if err != nil {
		return nil, err
	}
	if _, err := m.addDep(name, in.GitRepo, in.Ref, path.Clean(in.Subdir), paths); err != nil {
		return nil, fmt.Errorf("buf.gen.yaml: inputs[%d]: %w", i, err)
	}
	return []config.Input{{Dep: name, Paths: paths}}, nil
}

func (m *migration) directoryInput(i int, in bufInput) ([]config.Input, error) {
	dir := path.Clean(in.Directory)
	where := fmt.Sprintf("buf.gen.yaml: inputs[%d]", i)
	paths, err := rebase(where, dir, in.Paths)
	if err != nil {
		return nil, err
	}
	deps, vendored := m.vendored[dir]
	if !vendored {
		return []config.Input{{Module: dir, Paths: paths}}, nil
	}
	// The directory is a vendored tree. Its paths belong to whichever
	// dependency filled that part of the tree.
	if len(paths) == 0 {
		out := make([]config.Input, 0, len(deps))
		for _, name := range deps {
			out = append(out, config.Input{Dep: name})
		}
		if len(out) == 0 {
			m.unresolved("%s: directory %s is a vendored tree with no translatable source; declare the producing repository by hand", where, dir)
		}
		return out, nil
	}
	byDep := map[string][]string{}
	var order []string
	for _, p := range paths {
		name := m.ownerOf(p, deps)
		if name == "" {
			m.unresolved("%s: path %s selects files under the vendored tree %s, but no export invocation in the Makefile accounts for them; declare the producing repository by hand",
				where, path.Join(dir, p), dir)
			continue
		}
		if _, ok := byDep[name]; !ok {
			order = append(order, name)
		}
		byDep[name] = append(byDep[name], p)
	}
	out := make([]config.Input, 0, len(order))
	for _, name := range order {
		out = append(out, config.Input{Dep: name, Paths: byDep[name]})
	}
	return out, nil
}

// ownerOf decides which dependency a path under a vendored tree came from. An
// export that named --path says so directly; one that did not is matched by
// name, and a path matching neither is reported rather than assigned.
func (m *migration) ownerOf(p string, candidates []string) string {
	for _, name := range candidates {
		for _, ep := range m.depPaths[name] {
			if p == ep || strings.HasPrefix(p, ep+"/") || strings.HasPrefix(ep, p+"/") {
				return name
			}
		}
	}
	for _, name := range candidates {
		if len(m.depPaths[name]) > 0 {
			continue // it named its paths, and none of them matched
		}
		for _, seg := range strings.Split(p, "/") {
			if seg == name {
				return name
			}
		}
	}
	return ""
}

// rebase converts paths from workspace coordinates into module coordinates.
//
// This is the silent failure the whole package exists to prevent: in buf the
// paths of an input count from the workspace root, here from the module root.
// Carried across verbatim they select nothing, and selecting nothing is not
// visible in the output of a generator — it is visible only as code that
// stopped being regenerated.
func rebase(where, dir string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	dir = path.Clean(dir)
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = path.Clean(p)
		switch {
		case dir == "" || dir == ".":
			out = append(out, p)
		case p == dir:
			// The whole module; no narrowing left to express.
		case strings.HasPrefix(p, dir+"/"):
			out = append(out, strings.TrimPrefix(p, dir+"/"))
		default:
			return nil, fmt.Errorf("%s: path %s does not live under %s and cannot be expressed relative to it", where, p, dir)
		}
	}
	return out, nil
}

// depName derives a dependency name from a repository address: the last path
// segment without its .git suffix.
func depName(git string) string {
	s := strings.TrimSuffix(git, ".git")
	s = strings.TrimSuffix(s, "/")
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

func appendUnique(dst []string, add ...string) []string {
	for _, a := range add {
		if !contains(dst, a) {
			dst = append(dst, a)
		}
	}
	return dst
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func orNone(s string) string {
	if s == "" {
		return "(absent)"
	}
	return s
}
