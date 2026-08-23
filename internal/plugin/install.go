package plugin

import (
	"context"
	"debug/buildinfo"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Cache installs plugin binaries and keeps them.
//
// Three properties are the point of it, and each one is a defect this tool
// existed alongside before:
//
//   - The version is the manifest's, not the machine's. A plugin installed
//     here is the exact version the manifest declared, so two machines running
//     the same manifest run the same generator.
//   - An entry is immutable and shared. The key is module@version, so an
//     install is never overwritten in place, every repository that declares
//     the same version reuses one binary, and a run whose plugins are already
//     cached needs neither the network nor a toolchain.
//   - Nothing of the user's is touched. The binaries live under this tool's
//     own cache root; GOBIN is set to a directory inside it for the duration
//     of the install and PATH is never modified.
type Cache struct {
	// Root is the cache root — the same root the fetched repositories use.
	// Plugins live in a subdirectory of it, never mixed in with them.
	Root string
}

// dirName is the subdirectory of the cache root that holds installed plugins.
const dirName = "plugins"

// Ensure returns the path of the binary that module@version builds,
// installing it first if the cache does not already hold it.
//
// The installed binary is verified against the version that was asked for by
// reading the module metadata the linker stamped into it, on every call and
// not only after an install: a cache entry that cannot be shown to be the
// declared version is not evidence of anything, and a corrupted or
// hand-replaced one would otherwise be trusted forever.
func (c Cache) Ensure(ctx context.Context, module, version string) (string, error) {
	if c.Root == "" {
		return "", errors.New("plugin: no cache root for installing plugins")
	}
	if module == "" || version == "" {
		return "", errors.New("plugin: a managed plugin needs both a module and a version")
	}
	bin := c.path(module, version)
	if _, err := os.Stat(bin); err == nil {
		return bin, verify(bin, module, version)
	}
	if err := c.install(ctx, module, version, bin); err != nil {
		return "", err
	}
	return bin, verify(bin, module, version)
}

// Path is where module@version lives, whether or not it has been installed. It
// is exported so that a caller can report the provenance of a plugin without
// installing anything.
func (c Cache) Path(module, version string) string { return c.path(module, version) }

func (c Cache) path(module, version string) string {
	return filepath.Join(c.dir(module, version), binaryName(module))
}

func (c Cache) dir(module, version string) string {
	// The module path is a URL-shaped path with no traversal in it, so it maps
	// onto directories directly. The version is appended to the last element,
	// which is what makes two versions two entries rather than one racing one.
	return filepath.Join(c.Root, dirName, filepath.FromSlash(module)+"@"+version)
}

// binaryName is what `go install` calls the binary: the last element of the
// module path, which for a command under cmd/ is the command's own name.
func binaryName(module string) string {
	return filepath.Base(filepath.FromSlash(strings.TrimSuffix(module, "/")))
}

// install runs `go install module@version` into a staging directory and moves
// the result into place, so that a failed or interrupted install never leaves
// a half-written binary that a later run would take for a cache hit.
func (c Cache) install(ctx context.Context, module, version, bin string) error {
	goBin, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("installing %s@%s: no go toolchain on PATH, and it is not in the cache at %s; "+
			"install Go, or run the generation on a machine that has it and share the cache",
			module, version, filepath.Dir(bin))
	}
	if err := os.MkdirAll(filepath.Join(c.Root, dirName), 0o755); err != nil {
		return fmt.Errorf("installing %s@%s: %w", module, version, err)
	}
	stage, err := os.MkdirTemp(filepath.Join(c.Root, dirName), ".staging-")
	if err != nil {
		return fmt.Errorf("installing %s@%s: %w", module, version, err)
	}
	defer os.RemoveAll(stage)

	cmd := exec.CommandContext(ctx, goBin, "install", module+"@"+version)
	// GOBIN decides where the binary lands. It is set for this command only:
	// the user's own GOBIN is never written to and never read.
	cmd.Env = append(os.Environ(), "GOBIN="+stage)
	// Run outside any module, so that a repository's own go.mod, vendor
	// directory or toolchain line cannot change what gets installed.
	cmd.Dir = os.TempDir()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("installing %s@%s: %w", module, version, ctxErr)
		}
		return fmt.Errorf("installing %s@%s: %w\n%s\n"+
			"This step needs network access to the Go module proxy the first time a version is used. "+
			"Once installed it is cached at %s and later runs need neither the network nor a toolchain.",
			module, version, err, strings.TrimSpace(stderr.String()), filepath.Dir(bin))
	}

	staged := filepath.Join(stage, binaryName(module))
	if _, err := os.Stat(staged); err != nil {
		// A module whose main package is not where the path says lands here.
		return fmt.Errorf("installing %s@%s: the toolchain installed no binary named %q; "+
			"the module path must name a main package", module, version, binaryName(module))
	}
	// Verify before publishing: an entry that reaches the cache is an entry a
	// later run will trust without reinstalling.
	if err := verify(staged, module, version); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return fmt.Errorf("installing %s@%s: %w", module, version, err)
	}
	if err := os.Rename(staged, bin); err != nil {
		// A concurrent install of the same version is not a failure: the entry
		// is immutable, so whichever won produced the same bytes.
		if _, statErr := os.Stat(bin); statErr == nil {
			return nil
		}
		return fmt.Errorf("installing %s@%s: %w", module, version, err)
	}
	return nil
}

// verify reads the binary's own module metadata and refuses anything that is
// not the version that was asked for. Installation is checked rather than
// assumed on purpose: `go install` is being asked for a specific version, and
// the only evidence that it delivered one is in the artefact.
func verify(bin, module, version string) error {
	info, err := buildinfo.ReadFile(bin)
	if err != nil {
		return fmt.Errorf("%s at %s carries no Go module metadata, so it cannot be shown to be %s@%s: %w; "+
			"delete it and let the tool reinstall", bin, filepath.Dir(bin), module, version, err)
	}
	if info.Main.Version != version {
		return fmt.Errorf("%s was asked for at %s but the binary at %s reports %s; "+
			"delete it and let the tool reinstall", module, version, bin, info.Main.Version)
	}
	if !isPathOf(module, info.Main.Path) {
		return fmt.Errorf("%s at %s reports module %s, which is not %s; "+
			"delete it and let the tool reinstall", bin, version, info.Main.Path, module)
	}
	return nil
}

// isPathOf reports whether main is the module a command at pkg belongs to. The
// linker records the module, not the package, so a command under cmd/ reports
// the module that contains it.
func isPathOf(pkg, main string) bool {
	return pkg == main || strings.HasPrefix(pkg, main+"/")
}

// Origins a plugin binary can have — one per tier the manifest declares them
// on. They are recorded, not inferred, because the difference is the whole
// claim this tool makes about reproducibility: managed and url mean the
// manifest decided which bytes run, file and path mean the machine did.
const (
	// OriginManaged: installed from module@version by the Go toolchain.
	OriginManaged = "managed"
	// OriginURL: downloaded from a declared url and verified against a
	// declared sha256.
	OriginURL = "url"
	// OriginFile: an explicit path in the manifest, relative to it.
	OriginFile = "file"
	// OriginPath: a bare name, looked up on PATH.
	OriginPath = "path"
)

// Unknown is the version of a plugin whose version cannot be read. A plugin
// that is not a Go program carries no module metadata, and there is nothing to
// read; guessing would produce evidence that is worse than none, because it
// would be believed.
const Unknown = "unknown"

// Spec is one manifest plugin as the resolver takes it: the name, and the one
// tier the manifest declared. Validation of the tiers belongs to the manifest
// parser; what arrives here has at most one of them set.
type Spec struct {
	// Name is the manifest's own spelling of the plugin.
	Name string
	// Module and Version are the managed tier.
	Module, Version string
	// URL, SHA256 and ArchivePath are the download tier.
	URL, SHA256, ArchivePath string
	// Path is the explicit-path tier, as written in the manifest.
	Path string
	// Dir is the directory of the manifest. It is what Path is relative to,
	// and it is a parameter rather than the process's working directory so
	// that the same manifest resolves the same way wherever it is run from.
	Dir string
}

// Binary is a plugin resolved to something runnable, with its provenance.
type Binary struct {
	// Name is the manifest's own spelling of the plugin.
	Name string
	// Path is the executable to run.
	Path string
	// Module is the module it was installed from, for a managed plugin, and
	// empty otherwise.
	Module string
	// Version is the installed version for a managed plugin, and for a PATH
	// plugin whatever its own metadata reports — or Unknown.
	Version string
	// URL and SHA256 are what a downloaded plugin was pinned to.
	URL, SHA256 string
	// ArchivePath is the member taken from a downloaded archive, if any.
	ArchivePath string
	// Declared is the path as the manifest wrote it, for the file tier. Path
	// is where that resolved to; both are kept because the manifest's own
	// spelling is what a reader has to go and edit.
	Declared string
	// Origin is one of the four Origin constants.
	Origin string
	// Warning is a note about this resolution that is worth saying out loud
	// but is not a refusal. It is prose for a human, never parsed. Empty
	// means there is nothing to say.
	Warning string
}

// Resolve turns one manifest plugin into a runnable binary.
//
// The four tiers differ in who decided which bytes run, and the answer is
// carried back in Origin rather than left to be guessed from which fields
// happen to be set.
func (c Cache) Resolve(ctx context.Context, s Spec) (Binary, error) {
	if s.Name == "" {
		return Binary{}, errors.New("plugin: no plugin named")
	}
	switch {
	case s.Module != "":
		bin, err := c.Ensure(ctx, s.Module, s.Version)
		if err != nil {
			return Binary{}, fmt.Errorf("plugin %q: %w", s.Name, err)
		}
		return Binary{Name: s.Name, Path: bin, Module: s.Module, Version: s.Version, Origin: OriginManaged}, nil
	case s.URL != "":
		bin, err := c.EnsureURL(ctx, s.URL, s.SHA256, s.ArchivePath)
		if err != nil {
			return Binary{}, fmt.Errorf("plugin %q: %w", s.Name, err)
		}
		// A downloaded binary is pinned to a digest, not to a version. The
		// digest is the honest pin; reporting it as a version would put a
		// number in the report that nothing published ever used.
		return Binary{
			Name: s.Name, Path: bin, Version: Unknown,
			URL: s.URL, SHA256: strings.ToLower(s.SHA256), ArchivePath: s.ArchivePath,
			Origin: OriginURL,
		}, nil
	case s.Path != "":
		return resolvePath(s)
	}
	path, err := exec.LookPath(s.Name)
	if err != nil {
		if strings.ContainsRune(s.Name, filepath.Separator) {
			return Binary{}, fmt.Errorf("plugin %q: %w", s.Name, err)
		}
		return Binary{}, fmt.Errorf("plugin %q: not found in PATH: %w; "+
			"either install it, or declare where it comes from in the manifest — module and version for a Go plugin, "+
			"url and sha256 for a published binary, or path for one in this repository",
			s.Name, err)
	}
	return Binary{Name: s.Name, Path: path, Version: pathVersion(path), Origin: OriginPath}, nil
}

// resolvePath resolves the explicit-path tier.
//
// The path is interpreted relative to the manifest because a manifest is a
// shared, committed file: a path resolved against the working directory would
// mean a different binary depending on where the tool was invoked from, which
// is precisely the drift the other tiers exist to remove.
func resolvePath(s Spec) (Binary, error) {
	declared := filepath.FromSlash(s.Path)
	abs := declared
	warning := ""
	if filepath.IsAbs(declared) {
		// Accepted: someone may genuinely be pointing at a system binary.
		// Reported: in a file that is committed and shared, this line names a
		// binary that exists on exactly one machine.
		warning = fmt.Sprintf("plugin %q: path %s is absolute; in a manifest that is committed and shared "+
			"it resolves only on the machine it was written on. Prefer a path relative to the manifest, "+
			"or url and sha256 so the binary is fetched and pinned.", s.Name, declared)
	} else {
		abs = filepath.Join(s.Dir, declared)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Binary{}, fmt.Errorf("plugin %q: path %s does not resolve to a binary at %s: %w",
			s.Name, s.Path, abs, err)
	}
	if info.IsDir() {
		return Binary{}, fmt.Errorf("plugin %q: path %s resolves to the directory %s, not to a binary",
			s.Name, s.Path, abs)
	}
	// A path says where the binary is and nothing about which build it is.
	// There is no version to pin and none is claimed.
	return Binary{
		Name: s.Name, Path: abs, Declared: s.Path, Version: Unknown,
		Origin: OriginFile, Warning: warning,
	}, nil
}

// pathVersion reads the version a binary on PATH declares about itself, or
// Unknown when it declares none.
func pathVersion(path string) string {
	info, err := buildinfo.ReadFile(path)
	if err != nil || info.Main.Version == "" {
		return Unknown
	}
	return info.Main.Version
}
