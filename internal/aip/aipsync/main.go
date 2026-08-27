// Command aipsync derives the AIP inventory from upstream and writes it to
// internal/aip/index.tsv.
//
// It is maintainer tooling, not a subcommand of `stele`: nobody linting their
// own protos needs it, and a command that reaches the network from a lint run
// would be a surprise. It runs in two modes.
//
//	go run ./internal/aip/aipsync            # refresh the snapshot
//	go run ./internal/aip/aipsync -check     # fail if either is out of date
//
// `-check` is what CI runs, in the networked job, beside parity. The point of
// the split is that the *classification* test — every AIP has a disposition —
// stays hermetic and runs on every change, while only the question "has
// upstream moved" needs a clone.
//
// What happens when upstream moves:
//
//   - An AIP is added. The refresh writes it into index.tsv, and the hermetic
//     test fails naming the number and the title until somebody classifies
//     it in ledger.yaml. This command deliberately does not write that entry
//     itself, not even as `untriaged`: a generated placeholder is a green
//     build with an unread AIP behind it, and the whole mechanism is that
//     only a person can produce a disposition.
//   - An AIP changes state — approved to withdrawn, say. index.tsv changes,
//     `-check` fails, and the diff is in the review. The ledger keeps its
//     entry: what we decided about 142 does not stop being true because 142
//     was withdrawn, and deleting the entry would lose the reasoning.
//   - An AIP is renumbered or removed. It leaves index.tsv, and the hermetic
//     test fails on a ledger entry for an AIP that no longer exists — because
//     a rule named after a number that upstream reused would be pointing at
//     different guidance than the one it was written for, and that is exactly
//     the drift this inventory exists to catch.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/thegorangers/stele/internal/aip"
)

const upstream = "https://github.com/aip-dev/google.aip.dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "aipsync:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		check  = flag.Bool("check", false, "fail if the committed snapshot is not what upstream says")
		corpus = flag.String("corpus", "", "use this checkout of the upstream repository instead of cloning one")
		out    = flag.String("out", "internal/aip/index.tsv", "where the snapshot is written")
	)
	flag.Parse()

	dir, commit := *corpus, ""
	if dir == "" {
		tmp, err := os.MkdirTemp("", "aipsync")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		dir = filepath.Join(tmp, "corpus")
		if err := git("", "clone", "--depth", "1", "--quiet", upstream, dir); err != nil {
			return fmt.Errorf("clone %s: %w", upstream, err)
		}
	}
	commit, err := revParse(dir)
	if err != nil {
		return err
	}

	recs, err := aip.ParseCorpus(os.DirFS(dir))
	if err != nil {
		return err
	}

	var buf strings.Builder
	if err := aip.WriteIndex(&buf, upstream, commit, recs); err != nil {
		return err
	}

	old, readErr := os.ReadFile(*out)
	if *check {
		if readErr != nil {
			return fmt.Errorf("read %s: %w", *out, readErr)
		}
		if string(old) == buf.String() {
			fmt.Printf("aipsync: %s is what upstream says at %s (%d AIPs)\n", *out, commit[:12], len(recs))
			return nil
		}
		return fmt.Errorf("%s is not what upstream says at %s.\n"+
			"    Run `go run ./internal/aip/aipsync`, review the diff, and classify anything new in "+
			"internal/aip/ledger.yaml. An AIP that changed state upstream is a decision to revisit, "+
			"not a line to regenerate past", *out, commit[:12])
	}
	if readErr == nil && string(old) == buf.String() {
		fmt.Printf("aipsync: %s is already current at %s (%d AIPs)\n", *out, commit[:12], len(recs))
		return nil
	}
	if err := os.WriteFile(*out, []byte(buf.String()), 0o644); err != nil {
		return err
	}
	fmt.Printf("aipsync: wrote %s from %s at %s (%d AIPs)\n", *out, upstream, commit[:12], len(recs))
	return nil
}

func git(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func revParse(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s does not look like a checkout of %s: %w", dir, upstream, err)
	}
	return strings.TrimSpace(string(b)), nil
}
