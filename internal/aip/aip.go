// Package aip is the inventory of the AIP corpus this tool lints against, and
// the ledger that says what has been done about each AIP.
//
// # Why an inventory at all
//
// AIP is an external corpus. It is edited upstream, it grows, and an AIP can
// change state under a number that stays the same. A tool that claims to
// implement "the AIP guidance" therefore has a list, and the only question is
// whether that list is derived or typed. This repository has already paid for
// the typed answer four times over — a hand-written enumeration of facts that
// drifted from the facts and looked complete while it drifted — so the list
// here is derived: `index.tsv` is written by `aipsync` out of the upstream
// repository and is never edited by hand, and `ledger.yaml` is checked
// against it by a test that fails when the two disagree.
//
// The split matters. The index says what exists; the ledger says what we
// decided. Only the second is human judgement, and it cannot silently fall
// behind the first, because an AIP with no entry — or an entry for an AIP that
// upstream no longer carries — fails `go test ./internal/aip`.
package aip

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Record is one AIP as upstream carries it.
//
// Every field is read out of the file; none is inferred by this package and
// none is editable here. Title is included because a number on its own is not
// reviewable: an entry in the ledger saying "declined" against `142` means
// nothing to a reader who has to open a browser to learn what 142 is.
type Record struct {
	// ID is the AIP number, which is its permanent identity upstream and the
	// identity a rule ID borrows.
	ID int
	// Scope is the directory the AIP lives in: `general`, or one of the
	// vendor-specific sets.
	Scope string
	// State is upstream's own state word — approved, draft, reviewing, and
	// the ones AIP-1 defines but the corpus does not currently use
	// (withdrawn, rejected, replaced, deprecated).
	State string
	// Category is the front matter's placement category where there is one.
	// Only `general` uses it.
	Category string
	// Proto reports whether the body contains a fenced ```proto block. It is
	// a mechanical prior for "is this about protobuf API design at all", not
	// an answer: AIP-121 and AIP-130 are about proto APIs and contain no
	// proto, and AIP-4222 is about client libraries and contains some. The
	// ledger, not this flag, decides.
	Proto bool
	// Title is the first heading of the body.
	Title string
}

var (
	frontMatter = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n`)
	protoFence  = regexp.MustCompile("(?m)^```proto")
	heading     = regexp.MustCompile(`(?m)^#\s+(.+)$`)
)

// ParseCorpus reads every AIP under `aip/<scope>/*.md` of an upstream
// checkout, in ID order.
//
// A file that carries no front matter, no id or no state is an error rather
// than a skipped file. Upstream's own layout is the contract this reader
// depends on, and the failure mode worth avoiding is the one where a change
// to that layout quietly shrinks the inventory — a shorter list looks exactly
// like a complete one.
func ParseCorpus(root fs.FS) ([]Record, error) {
	scopes, err := fs.ReadDir(root, "aip")
	if err != nil {
		return nil, fmt.Errorf("read the corpus: %w", err)
	}
	var out []Record
	for _, s := range scopes {
		if !s.IsDir() {
			continue
		}
		dir := path.Join("aip", s.Name())
		entries, err := fs.ReadDir(root, dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := path.Join(dir, e.Name())
			b, err := fs.ReadFile(root, name)
			if err != nil {
				return nil, err
			}
			r, err := parseAIP(s.Name(), string(b))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the corpus contains no AIPs; this is a layout change upstream, not an empty corpus")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	for i := 1; i < len(out); i++ {
		if out[i].ID == out[i-1].ID {
			return nil, fmt.Errorf("AIP %d is declared twice, in %s and %s",
				out[i].ID, out[i-1].Scope, out[i].Scope)
		}
	}
	return out, nil
}

func parseAIP(scope, text string) (Record, error) {
	m := frontMatter.FindStringSubmatch(text)
	if m == nil {
		return Record{}, fmt.Errorf("no front matter")
	}
	head, body := m[1], text[len(m[0]):]
	r := Record{
		Scope:    scope,
		State:    scalar(head, "state"),
		Category: scalar(head, "category"),
		Proto:    protoFence.MatchString(body),
	}
	id := scalar(head, "id")
	if id == "" {
		return Record{}, fmt.Errorf("front matter declares no id")
	}
	n, err := strconv.Atoi(id)
	if err != nil {
		return Record{}, fmt.Errorf("front matter declares id %q, which is not a number", id)
	}
	r.ID = n
	if r.State == "" {
		return Record{}, fmt.Errorf("front matter declares no state")
	}
	if h := heading.FindStringSubmatch(body); h != nil {
		r.Title = strings.TrimSpace(h[1])
	}
	if r.Title == "" {
		return Record{}, fmt.Errorf("the body has no heading")
	}
	return r, nil
}

// scalar reads `key: value` out of front matter. It is not a YAML parser and
// does not need to be: the four keys read here are scalars written on one
// line, and a key that stops being one shows up as a parse error naming the
// file rather than as a wrong value.
func scalar(head, key string) string {
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(key) + `:\s*(\S+)\s*$`)
	if m := re.FindStringSubmatch(head); m != nil {
		return strings.Trim(m[1], `"'`)
	}
	return ""
}

// Index is a derived snapshot of the corpus, together with where it came
// from.
//
// The snapshot is committed so that the ledger test is hermetic: the check
// that every AIP is classified runs in the same offline job as everything
// else, and only the refresh needs the network. The commit is recorded so
// that "what upstream said when we last looked" is a fact in the repository
// rather than a memory.
type Index struct {
	Source  string
	Commit  string
	Records []Record
}

const indexHeader = "id\tscope\tstate\tcategory\tproto\ttitle"

// WriteIndex writes the snapshot in the form `index.tsv` is committed in.
func WriteIndex(w io.Writer, src, commit string, recs []Record) error {
	b := bufio.NewWriter(w)
	fmt.Fprintf(b, "# Derived by internal/aip/aipsync. Do not edit by hand.\n")
	fmt.Fprintf(b, "# source\t%s\n", src)
	fmt.Fprintf(b, "# commit\t%s\n", commit)
	fmt.Fprintf(b, "%s\n", indexHeader)
	for _, r := range recs {
		proto := "no"
		if r.Proto {
			proto = "yes"
		}
		fmt.Fprintf(b, "%d\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Scope, r.State, r.Category, proto, r.Title)
	}
	return b.Flush()
}

// ParseIndex reads a snapshot back.
func ParseIndex(r io.Reader) (Index, error) {
	var ix Index
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	seenHeader := false
	for line := 1; sc.Scan(); line++ {
		t := sc.Text()
		switch {
		case strings.HasPrefix(t, "# source\t"):
			ix.Source = strings.TrimPrefix(t, "# source\t")
			continue
		case strings.HasPrefix(t, "# commit\t"):
			ix.Commit = strings.TrimPrefix(t, "# commit\t")
			continue
		case strings.HasPrefix(t, "#"), t == "":
			continue
		case t == indexHeader:
			seenHeader = true
			continue
		}
		f := strings.Split(t, "\t")
		if len(f) != 6 {
			return Index{}, fmt.Errorf("line %d has %d columns, not 6", line, len(f))
		}
		id, err := strconv.Atoi(f[0])
		if err != nil {
			return Index{}, fmt.Errorf("line %d: %q is not an AIP number", line, f[0])
		}
		ix.Records = append(ix.Records, Record{
			ID: id, Scope: f[1], State: f[2], Category: f[3], Proto: f[4] == "yes", Title: f[5],
		})
	}
	if err := sc.Err(); err != nil {
		return Index{}, err
	}
	if !seenHeader {
		return Index{}, fmt.Errorf("the snapshot has no column header, so it is not one")
	}
	if ix.Commit == "" || ix.Source == "" {
		return Index{}, fmt.Errorf("the snapshot does not say which upstream commit it was derived from")
	}
	return ix, nil
}
