// Package report records what produced the bytes of a generation run.
//
// Generated code is reproducible only if what generated it can be named. The
// obvious half of that is the plugins, and a report that listed only them
// would look complete while being wrong: the descriptor the plugins are handed
// is parsed by protocompile, serialised by protobuf-go and assembled by stele
// itself, and a change in any of the three changes the output without any
// plugin moving. So all of them are components here, on equal footing.
//
// Two rules run through the file, and both are about not lying:
//
//   - A version that cannot be obtained is Unknown. A plugin that is not a Go
//     binary — protoc-gen-dart is the one we have — carries no module
//     metadata, and there is nothing to read. Guessing would produce evidence
//     that is worse than no evidence, because it would be believed.
//   - A component is never dropped. A plugin missing from the report reads as
//     a plugin that did not run, which is a different claim about the run than
//     "it ran and I could not version it".
//
// Plugin versions are read from the module metadata the Go linker stamps into
// the binary — the same metadata `go version -m` prints, read here with the
// standard library's debug/buildinfo so that reporting does not need a Go
// toolchain on the machine. The alternative, asking the plugin, was measured
// against the four plugins in the supported surface and works for one:
// protoc-gen-go answers, protoc-gen-go-grpc answers without a leading v,
// protoc-gen-go-vtproto refuses, and protoc-gen-dart emits binary noise and
// hangs unless its stdin is closed. Reading beats asking, and it reaches
// pseudo-versions besides.
package report

import (
	"bytes"
	"debug/buildinfo"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
)

// Unknown is the version of a component whose version could not be obtained.
// It is a value, not an absence: see the package comment.
const Unknown = "unknown"

// devel is what the Go toolchain stamps into a binary built from a checkout
// rather than from a module version. It is reported verbatim: a development
// build saying so is evidence, a development build claiming a release version
// is a trap.
const devel = "(devel)"

// version is stele's own version. A release build overrides it with
//
//	go build -ldflags "-X github.com/thegorangers/stele/internal/report.version=v1.2.3"
//
// and every other build falls back to the module metadata of the running
// binary, which says (devel) when there is nothing better to say.
var version string

// The modules whose versions decide the descriptor, and the names they are
// reported under. The keys are import paths so that the lookup cannot drift
// from what the build actually depends on.
const (
	protocompileModule = "github.com/bufbuild/protocompile"
	protobufGoModule   = "google.golang.org/protobuf"
)

// Kinds of component, so a reader (and a diff) can tell an engine from a
// plugin without knowing the names.
const (
	KindTool    = "tool"
	KindLibrary = "library"
	KindPlugin  = "plugin"
)

// Component is one thing that determined the bytes of a run.
type Component struct {
	// Kind is tool, library or plugin.
	Kind string `json:"kind"`
	// Module is the module path the version belongs to, when there is one.
	Module string `json:"module,omitempty"`
	// Version is the version, or Unknown.
	Version string `json:"version"`
	// Path is where a plugin binary was found, when it was found. Two runs
	// with the same plugin name and different binaries are a real cause of
	// differing output, and the name alone would hide it.
	Path string `json:"path,omitempty"`
	// Note says why a version is Unknown. It is prose for a human reading the
	// evidence, never parsed.
	Note string `json:"note,omitempty"`
}

// Report is the set of components a run went through, keyed by the name each
// is known by: "stele", "protocompile", "protobuf-go", and one entry per
// plugin under the name the manifest wrote.
type Report struct {
	Components map[string]Component `json:"components"`
}

// Build assembles the report for a run that invoked the named plugins.
//
// Duplicates among plugins collapse: a plugin named by two targets is one
// binary and one version. Nothing here fails: a report is evidence about a run
// that already happened, and refusing to produce it because one plugin could
// not be versioned would destroy the evidence about the rest.
func Build(plugins []string) *Report {
	r := &Report{Components: make(map[string]Component, len(plugins)+3)}

	r.Components["stele"] = Component{Kind: KindTool, Module: modulePath(), Version: steleVersion()}
	for name, mod := range map[string]string{
		"protocompile": protocompileModule,
		"protobuf-go":  protobufGoModule,
	} {
		c := Component{Kind: KindLibrary, Module: mod, Version: Unknown}
		if v, ok := dependencyVersion(mod); ok {
			c.Version = v
		} else {
			// A binary that does not link the module, and any test binary —
			// the toolchain stamps no dependency list into those — land here.
			c.Note = "not among the running binary's recorded module dependencies"
		}
		r.Components[name] = c
	}

	for _, bin := range plugins {
		if bin == "" {
			continue
		}
		name := pluginName(bin)
		if _, seen := r.Components[name]; seen {
			continue
		}
		r.Components[name] = describePlugin(bin)
	}
	return r
}

// JSON renders the report for machine comparison: sorted keys, indented, no
// timestamp and no host detail, so that two runs that used the same versions
// produce byte-identical reports and a diff shows only what changed. It ends
// in a newline, because it is written to files and to terminals.
func (r *Report) JSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	// encoding/json sorts map keys, which is the whole reason the components
	// are a map and not a slice.
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Summary renders the report as one line per component, for stderr. The JSON
// is the artefact; this is so that a run that was not asked to write a file
// still says out loud what produced its output.
func (r *Report) Summary() string {
	names := make([]string, 0, len(r.Components))
	for n := range r.Components {
		names = append(names, n)
	}
	sort.Strings(names)
	width := 0
	for _, n := range names {
		if len(n) > width {
			width = len(n)
		}
	}
	var b strings.Builder
	b.WriteString("stele: versions that determined this run's output\n")
	for _, n := range names {
		c := r.Components[n]
		fmt.Fprintf(&b, "  %-*s  %s", width, n, c.Version)
		if c.Module != "" && c.Module != n {
			fmt.Fprintf(&b, "  (%s)", c.Module)
		}
		if c.Note != "" {
			fmt.Fprintf(&b, "  [%s]", c.Note)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// describePlugin reads a plugin's module metadata, reporting honestly when
// there is none to read.
func describePlugin(bin string) Component {
	c := Component{Kind: KindPlugin, Version: Unknown}
	path, err := exec.LookPath(bin)
	if err != nil {
		c.Note = "not found: " + err.Error()
		return c
	}
	c.Path = path
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		// A plugin that is not a Go binary lands here, and so does one whose
		// metadata was stripped. Both are the same answer to the only
		// question asked: the version is not obtainable from the binary.
		c.Note = "no Go module metadata in the binary: " + err.Error()
		return c
	}
	c.Module = info.Main.Path
	c.Version = info.Main.Version
	if c.Version == "" {
		c.Version = Unknown
		c.Note = "the binary carries a module path but no version"
	}
	return c
}

// pluginName is the name a plugin is reported under: the base name of the
// binary, so that a manifest naming a plugin by path and one naming it on PATH
// produce comparable reports.
func pluginName(bin string) string {
	return filepath.Base(filepath.FromSlash(bin))
}

// steleVersion is the ldflags override when there is one, and the running
// binary's own module version otherwise.
func steleVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return devel
}

// modulePath is the module path of the running binary, for the same reason a
// plugin's is recorded: it names what the version belongs to.
func modulePath() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		return info.Main.Path
	}
	return ""
}

// dependencyVersion finds the version of a module this binary was built
// against. Replaced modules report the replacement's version, which is the one
// whose code actually ran.
func dependencyVersion(mod string) (string, bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	for _, d := range info.Deps {
		if d.Path != mod {
			continue
		}
		if d.Replace != nil && d.Replace.Version != "" {
			return d.Replace.Version, true
		}
		if d.Version == "" {
			return "", false
		}
		return d.Version, true
	}
	return "", false
}
