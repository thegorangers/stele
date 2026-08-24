package migrate

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/thegorangers/stele/internal/config"
)

// YAML renders the migrated manifest for review.
//
// The output is meant to be read by a human before it is committed, so it
// carries the header of what was dropped and what is still open, and marks
// every entry a decision was left in. Machine-clean output would hide exactly
// the parts that need a person.
func (r *Result) YAML() ([]byte, error) {
	var doc yaml.Node
	if err := doc.Encode(shadowOf(r.File)); err != nil {
		return nil, err
	}
	doc.HeadComment = r.header()
	annotate(&doc, r.File)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// header is the comment block at the top of the manifest.
func (r *Result) header() string {
	lines := []string{
		"Migrated from buf.yaml and buf.gen.yaml by `stele migrate`.",
		"Review it: the translation covers a subset, and the rest is listed here.",
	}
	if len(r.Unresolved) > 0 {
		lines = append(lines, "", "UNRESOLVED — this manifest is incomplete until each of these is decided:")
		for _, u := range r.Unresolved {
			lines = append(lines, "  - "+u)
		}
	}
	if len(r.Notes) > 0 {
		lines = append(lines, "", "Dropped on purpose (no counterpart in this tool):")
		for _, n := range r.Notes {
			lines = append(lines, "  - "+n)
		}
	}
	return strings.Join(lines, "\n")
}

// annotate marks the entries a human still has to complete.
func annotate(doc *yaml.Node, f *config.File) {
	annotateDeps(doc, f)
	annotatePlugins(doc, f)
}

func annotateDeps(doc *yaml.Node, f *config.File) {
	deps := child(doc, "deps")
	if deps == nil {
		return
	}
	for i, d := range f.Deps {
		if i >= len(deps.Content) {
			continue
		}
		if d.Ref != "" {
			continue
		}
		deps.Content[i].HeadComment = fmt.Sprintf(
			"TODO: pin a commit. `buf export` took the default branch of %s\nand recorded nothing, so there is no ref to carry over.", d.Git)
	}
}

// annotatePlugins marks every plugin left resolving off PATH. The manifest is
// the place a reviewer looks, and an unpinned plugin is invisible there: it is
// spelled exactly like a pinned one, minus two lines nobody misses.
func annotatePlugins(doc *yaml.Node, f *config.File) {
	targets := child(doc, "generate")
	if targets == nil {
		return
	}
	for i, g := range f.Generate {
		if i >= len(targets.Content) {
			return
		}
		plugins := child(targets.Content[i], "plugins")
		if plugins == nil {
			continue
		}
		for j, p := range g.Plugins {
			if j >= len(plugins.Content) || p.Module != "" || len(p.Downloads) > 0 || p.Path != "" {
				continue
			}
			plugins.Content[j].HeadComment = fmt.Sprintf(
				"TODO: say where %s comes from. No version was recovered, so this\nresolves off PATH and generates whatever that machine happens to have.", p.Local)
		}
	}
}

// child returns the value node of key in a mapping node.
func child(n *yaml.Node, key string) *yaml.Node {
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// The shadow types below exist so that an absent field is absent from the
// output rather than written as an empty value. config's own types are the
// parsed shape and carry no omitempty, because a manifest is read there, not
// written.

type outFile struct {
	Version  int         `yaml:"version"`
	Modules  []outModule `yaml:"modules,omitempty"`
	Deps     []outDep    `yaml:"deps,omitempty"`
	Generate []outTarget `yaml:"generate"`
}

type outModule struct {
	Path string `yaml:"path"`
}

type outDep struct {
	Name   string   `yaml:"name"`
	Git    string   `yaml:"git"`
	Ref    string   `yaml:"ref"`
	Module string   `yaml:"module,omitempty"`
	Paths  []string `yaml:"paths,omitempty"`
}

type outTarget struct {
	Name    string      `yaml:"name"`
	Inputs  []outInput  `yaml:"inputs"`
	Plugins []outPlugin `yaml:"plugins"`
	Managed *outManaged `yaml:"managed,omitempty"`
}

type outInput struct {
	Module string   `yaml:"module,omitempty"`
	Dep    string   `yaml:"dep,omitempty"`
	Paths  []string `yaml:"paths,omitempty"`
}

type outPlugin struct {
	Local   string   `yaml:"local"`
	Module  string   `yaml:"module,omitempty"`
	Version string   `yaml:"version,omitempty"`
	Out     string   `yaml:"out"`
	Opt     []string `yaml:"opt,omitempty"`
}

type outManaged struct {
	Override []outOverride `yaml:"override"`
}

type outOverride struct {
	FileOption string `yaml:"file_option"`
	Path       string `yaml:"path,omitempty"`
	Value      string `yaml:"value"`
}

func shadowOf(f *config.File) outFile {
	out := outFile{Version: f.Version}
	for _, m := range f.Modules {
		out.Modules = append(out.Modules, outModule{Path: m.Path})
	}
	for _, d := range f.Deps {
		out.Deps = append(out.Deps, outDep{Name: d.Name, Git: d.Git, Ref: d.Ref, Module: d.Module, Paths: d.Paths})
	}
	for _, g := range f.Generate {
		t := outTarget{Name: g.Name}
		for _, in := range g.Inputs {
			t.Inputs = append(t.Inputs, outInput{Module: in.Module, Dep: in.Dep, Paths: in.Paths})
		}
		for _, p := range g.Plugins {
			t.Plugins = append(t.Plugins, outPlugin{Local: p.Local, Module: p.Module, Version: p.Version, Out: p.Out, Opt: p.Opt})
		}
		if g.Managed != nil {
			mg := &outManaged{}
			for _, o := range g.Managed.Override {
				mg.Override = append(mg.Override, outOverride{FileOption: o.FileOption, Path: o.Path, Value: o.Value})
			}
			t.Managed = mg
		}
		out.Generate = append(out.Generate, t)
	}
	return out
}
