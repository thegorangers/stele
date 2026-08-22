// Package config parses and validates the stele.yaml manifest.
package config

// File is a parsed stele.yaml manifest.
type File struct {
	// Version of the manifest format. Only 1 is supported.
	Version int `yaml:"version"`
	// Modules are the proto modules owned by this repository. Each path is an
	// import root: imports are resolved relative to it.
	Modules []Module `yaml:"modules"`
	// Deps are proto modules taken from other repositories.
	Deps []Dep `yaml:"deps"`
	// Generate lists the code generation targets.
	Generate []GenTarget `yaml:"generate"`
}

// Module is a proto module owned by this repository.
type Module struct {
	// Path is the directory of the module, relative to the manifest.
	Path string `yaml:"path"`
}

// Dep is a proto module taken from another repository.
type Dep struct {
	// Name identifies the dependency inside this manifest.
	Name string `yaml:"name"`
	// Git is the address of the producing repository.
	Git string `yaml:"git"`
	// Ref is the commit SHA or, for updating runs, a branch or tag.
	Ref string `yaml:"ref"`
	// Module is the module of the producer to take, relative to its root.
	Module string `yaml:"module"`
	// Paths narrows what is taken, in coordinates relative to the root of the
	// producer's module. Accepts a single string or a list of strings.
	Paths []string `yaml:"paths"`
}

// GenTarget is one code generation target.
type GenTarget struct {
	// Name identifies the target on the command line.
	Name string `yaml:"name"`
	// Inputs selects the files to generate from.
	Inputs []Input `yaml:"inputs"`
	// Plugins are the code generators to run over those files.
	Plugins []Plugin `yaml:"plugins"`
}

// Input selects files of a local module for generation.
type Input struct {
	// Module is the path of a module declared in Modules.
	Module string `yaml:"module"`
	// Paths narrows the selection, in coordinates relative to the root of that
	// module. Accepts a single string or a list of strings.
	Paths []string `yaml:"paths"`
}

// Plugin is one code generator invocation.
type Plugin struct {
	// Local is the name of, or the path to, an executable plugin binary.
	Local string `yaml:"local"`
	// Out is the directory the plugin writes to.
	Out string `yaml:"out"`
	// Opt are the plugin options. Accepts a single string or a list of strings.
	Opt []string `yaml:"opt"`
}
