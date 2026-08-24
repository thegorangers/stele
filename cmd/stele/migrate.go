package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/thegorangers/stele/internal/config/migrate"
)

const migrateUsage = `stele migrate translates a buf configuration into stele.yaml.

It reads buf.gen.yaml, buf.yaml if there is one, the Makefile — the only place
a vendored third-party tree records where it came from — and the repository's
own .proto files.

The protos are what decide the dependency set. A vendor target says which
modules were copied in, not which are read, and the two are not the same
number: one export of one registry module can bring in dozens of files where
one is imported. So the imports are followed transitively, the well-known
types the compiler carries are subtracted, and what is left is demanded file
by file — which is also the paths: narrowing each dependency is emitted with.
A vendored tree nothing imports is reported as drift and left out.

The translation covers a measured subset of the buf format. Anything outside
it is refused by name rather than approximated: a manifest that looks migrated
and is not is worse than no manifest at all. For the same reason a migration
that leaves anything for a human to decide exits non-zero, and the reasons are
printed and repeated in the manifest itself.

Dependency addresses are authored over https://, whatever form the Makefile
used. A typical CI image has no ssh binary, and https:// is the form both CI
and a workstation rewrite with 'git config insteadOf' — so the emitted
manifest needs no editing to clone over ssh. Every rewrite is reported.

By default the manifest is printed for review and nothing is written.

Usage:
  stele migrate [flags]

Flags:
  --dir DIR    directory holding the buf configuration (default ".")
  --write      write DIR/stele.yaml instead of printing it. An existing file
               is not overwritten.
  --force      overwrite an existing stele.yaml
`

func runMigrate(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	var (
		dir   = fs.String("dir", ".", "directory holding the buf configuration")
		write = fs.Bool("write", false, "write DIR/stele.yaml instead of printing it")
		force = fs.Bool("force", false, "overwrite an existing stele.yaml")
		help  = fs.Bool("help", false, "print the flags of this command")
	)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n\n%s", err, migrateUsage)
	}
	if *help {
		fmt.Fprint(stdout, migrateUsage)
		return errHelp
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q\n\n%s", fs.Arg(0), migrateUsage)
	}

	r, err := migrate.FromDir(*dir)
	if err != nil {
		return err
	}
	out, err := r.YAML()
	if err != nil {
		return err
	}

	for _, n := range r.Notes {
		fmt.Fprintf(stderr, "stele migrate: dropped: %s\n", n)
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(stderr, "stele migrate: warning: %s\n", w)
	}
	for _, u := range r.Unresolved {
		fmt.Fprintf(stderr, "stele migrate: unresolved: %s\n", u)
	}

	if *write {
		path := filepath.Join(*dir, "stele.yaml")
		if _, err := os.Stat(path); err == nil && !*force {
			return fmt.Errorf("%s already exists; pass --force to overwrite it", path)
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(stderr, "stele migrate: wrote %s\n", path)
	} else {
		if _, err := stdout.Write(out); err != nil {
			return err
		}
	}

	if !r.Complete() {
		// Exiting zero here would let an incomplete manifest travel as a
		// finished one, which is the failure this command exists to prevent.
		return fmt.Errorf("the migration is incomplete; %d item(s) need a decision:\n  - %s",
			len(r.Unresolved), strings.Join(r.Unresolved, "\n  - "))
	}
	return nil
}
