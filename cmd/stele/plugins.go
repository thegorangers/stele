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

A plugin that declares a module and a version is installed into stele's own
cache and run from there. A plugin that declares neither is looked up on PATH,
which is the only thing that can be done with a plugin that is not a Go
program; where it came from is stated rather than glossed over.

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
	return listPlugins(cfg, cache, stdout)
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
			k := p.Local + "\x00" + p.Module + "@" + p.Version
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
func listPlugins(cfg *config.File, cache plugin.Cache, stdout io.Writer) error {
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
	b, err := plugin.Cache{}.Resolve(context.Background(), path, "", "")
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
		if p.Module == "" {
			fmt.Fprintf(stdout, "%s: taken from PATH, not installed by stele\n", p.Local)
			continue
		}
		bin, err := cache.Ensure(ctx, p.Module, p.Version)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s: %s@%s at %s\n", p.Local, p.Module, p.Version, bin)
	}
	return nil
}
