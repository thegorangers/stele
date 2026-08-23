package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/thegorangers/stele/internal/cachedir"
	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/gen"
	"github.com/thegorangers/stele/internal/plugin"
)

// The command exists alongside install-on-demand, not instead of it.
// Generation installs whatever it needs, so nothing has to be run first and a
// fresh checkout works. What this adds is the two things on-demand cannot do:
// warm the cache in a step that is allowed to reach the network, so the
// generating step can run with none, and answer "which binary would run here"
// without running anything.
const pluginsUsage = `stele plugins manages the code generation plugins the manifest declares.

A plugin declares where it comes from, in one of four ways, and the listing
says which. A module and a version are installed into stele's own cache with
the Go toolchain. A url and a sha256 are downloaded into it and verified
against the digest before anything is run, which is how a plugin that is not a
Go program is pinned. A path names a binary in this repository, relative to the
manifest. A plugin that declares none of these is looked up on PATH. Nothing is
glossed over: what stele did not choose, it says it did not choose.

Nothing here is required: stele generate installs what it needs. This exists so
that a cache can be warmed where the network is available, and so that the
question of which binary would run can be asked without running it.

Usage:
  stele plugins list [flags]
  stele plugins install [flags]

Flags:
  --dir DIR         directory holding stele.yaml (default ".")
  --cache-dir DIR   where installed plugins are kept
                    (default $XDG_CACHE_HOME/stele, else ~/.cache/stele;
                    $STELE_CACHE_DIR is honoured too)
`

func runPlugins(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("stele plugins needs a subcommand: list or install\n\n%s", pluginsUsage)
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, pluginsUsage)
		return errHelp
	case "list", "install":
	default:
		return fmt.Errorf("unknown subcommand %q; stele plugins takes list or install\n\n%s", args[0], pluginsUsage)
	}
	install := args[0] == "install"

	fs := flag.NewFlagSet("plugins "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	var (
		dir      = fs.String("dir", ".", "directory holding stele.yaml")
		cacheDir = fs.String("cache-dir", "", "where installed plugins are kept")
		help     = fs.Bool("help", false, "show this help")
	)
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, pluginsUsage)
			return errHelp
		}
		return fmt.Errorf("%w\n\n%s", err, pluginsUsage)
	}
	if *help {
		fmt.Fprint(stdout, pluginsUsage)
		return errHelp
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("unexpected argument %q; stele plugins takes flags only\n\n%s", rest[0], pluginsUsage)
	}

	cfg, err := config.Load(filepath.Join(*dir, gen.ManifestName))
	if err != nil {
		return err
	}
	root, err := cachedir.Root(*cacheDir)
	if err != nil {
		return err
	}
	cache := plugin.Cache{Root: root}

	if install {
		return installPlugins(ctx, cfg, cache, stdout)
	}
	return listPlugins(cfg, *dir, cache, stdout)
}

// declaredPlugins returns every plugin the manifest declares, once each, in
// manifest order. The whole manifest is read, not one target: a cache that was
// warmed for the target that happened to be named would still send the next
// run to the network.
func declaredPlugins(cfg *config.File) []config.Plugin {
	var out []config.Plugin
	seen := map[string]bool{}
	for _, t := range cfg.Generate {
		for _, p := range t.Plugins {
			k := strings.Join([]string{p.Local, p.Module, p.Version, p.URL, p.SHA256, p.ArchivePath, p.Path}, "\x00")
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, p)
		}
	}
	return out
}

// listPlugins says, for each declared plugin, which binary would run and where
// it comes from — without installing anything, so that the answer describes
// the machine as it is.
func listPlugins(cfg *config.File, dir string, cache plugin.Cache, stdout io.Writer) error {
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PLUGIN\tSOURCE\tVERSION\tSTATE")
	for _, p := range declaredPlugins(cfg) {
		switch {
		case p.Module != "":
			state := "installed"
			if _, err := os.Stat(cache.Path(p.Module, p.Version)); err != nil {
				state = "not installed (stele plugins install, or the next generate, fetches it)"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.Local, p.Module, p.Version, state)
		case p.URL != "":
			state := "downloaded"
			if _, err := os.Stat(cache.URLPath(p.URL, p.SHA256, p.ArchivePath)); err != nil {
				state = "not downloaded (stele plugins install, or the next generate, fetches it)"
			}
			// The digest is the pin, so it is what the listing shows in the
			// column a version would occupy for a managed plugin.
			fmt.Fprintf(w, "%s\t%s\tsha256:%s\t%s\n", p.Local, p.URL, p.SHA256, state)
		case p.Path != "":
			at := p.Path
			if !filepath.IsAbs(at) {
				at = filepath.Join(dir, at)
			}
			state := at
			if _, err := os.Stat(at); err != nil {
				state = "not found at " + at
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.Local, "path "+p.Path, plugin.Unknown, state)
		default:
			path, err := exec.LookPath(p.Local)
			if err != nil {
				fmt.Fprintf(w, "%s\tPATH\t%s\tnot found on PATH\n", p.Local, plugin.Unknown)
				continue
			}
			fmt.Fprintf(w, "%s\tPATH\t%s\t%s\n", p.Local, pathVersionOf(path), path)
		}
	}
	return w.Flush()
}

// pathVersionOf is the version a binary on PATH reports about itself. It goes
// through the resolver so that the listing and a run can never describe the
// same binary differently.
func pathVersionOf(path string) string {
	b, err := plugin.Cache{}.Resolve(context.Background(), plugin.Spec{Name: path})
	if err != nil {
		return plugin.Unknown
	}
	return b.Version
}

// installPlugins fetches every declared plugin, so that a later run needs no
// network. A plugin with no module is not installed and says so: this tool
// does not pretend to manage a binary it cannot build.
func installPlugins(ctx context.Context, cfg *config.File, cache plugin.Cache, stdout io.Writer) error {
	for _, p := range declaredPlugins(cfg) {
		switch {
		case p.Module != "":
			bin, err := cache.Ensure(ctx, p.Module, p.Version)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s: %s@%s at %s\n", p.Local, p.Module, p.Version, bin)
		case p.URL != "":
			bin, err := cache.EnsureURL(ctx, p.URL, p.SHA256, p.ArchivePath)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s: %s verified against sha256:%s at %s\n", p.Local, p.URL, p.SHA256, bin)
		case p.Path != "":
			fmt.Fprintf(stdout, "%s: taken from the path %s, not installed by stele\n", p.Local, p.Path)
		default:
			fmt.Fprintf(stdout, "%s: taken from PATH, not installed by stele\n", p.Local)
		}
	}
	return nil
}
