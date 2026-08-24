package migrate

import (
	"fmt"
	"path"
	"strings"
)

// bufExport is one recovered `buf export` invocation. A vendored third-party
// tree does not record where it came from; the Makefile that fills it does,
// and it is the only place the source is written down.
type bufExport struct {
	// Target is the argument as it was written, for messages.
	Target string
	// Git is the repository address, empty for a registry reference.
	Git string
	// Ref is the pinned commit, if the invocation pinned one. `buf export`
	// takes the default branch when it does not, so this is usually empty.
	Ref string
	// Subdir is the module of the producer that was exported.
	Subdir string
	// Paths are the --path arguments, already relative to the producer's
	// module root.
	Paths []string
	// Output is the directory the tree was written into. It identifies the
	// vendored tree in buf.yaml and in the generate inputs.
	Output string
	// Registry is set when the target names a schema registry rather than a
	// git repository. Such a reference cannot be translated.
	Registry bool
}

// registryHost is the schema registry this tool deliberately never contacts.
// A reference to it is reported, never resolved.
const registryHost = "buf.build/"

// parseExports recovers the export invocations from a Makefile. It is
// deliberately defensive: an invocation it does not fully understand is
// reported, not guessed at.
func parseExports(makefile []byte) ([]bufExport, []string, error) {
	var (
		out      []bufExport
		unparsed []string
	)
	lines, err := logicalLines(string(makefile))
	if err != nil {
		return nil, nil, err
	}
	for _, line := range lines {
		if !strings.Contains(line, exportInvocation) {
			continue
		}
		args, err := fields(line)
		if err != nil {
			unparsed = append(unparsed, fmt.Sprintf("%s: %v", strings.TrimSpace(line), err))
			continue
		}
		i := indexOfExport(args)
		if i < 0 {
			continue
		}
		e, err := parseExport(args[i+1:])
		if err != nil {
			unparsed = append(unparsed, fmt.Sprintf("%s: %v", strings.TrimSpace(line), err))
			continue
		}
		out = append(out, e)
	}
	return out, unparsed, nil
}

// indexOfExport finds the "export" word of a `buf export` invocation.
func indexOfExport(args []string) int {
	for i := 0; i+1 < len(args); i++ {
		if strings.TrimPrefix(strings.TrimPrefix(args[i], "@"), "-") == "buf" ||
			path.Base(args[i]) == "buf" {
			if args[i+1] == "export" {
				return i + 1
			}
		}
	}
	return -1
}

func parseExport(args []string) (bufExport, error) {
	var e bufExport
	for i := 0; i < len(args); i++ {
		a := args[i]
		// value returns the value of a flag written either as --flag=value or
		// as --flag value.
		value := func(name string) (string, bool, error) {
			if a == name {
				if i+1 >= len(args) {
					return "", true, fmt.Errorf("%s: missing value", name)
				}
				i++
				return args[i], true, nil
			}
			if v, ok := strings.CutPrefix(a, name+"="); ok {
				return v, true, nil
			}
			return "", false, nil
		}
		switch {
		case !strings.HasPrefix(a, "-"):
			if e.Target != "" {
				return e, fmt.Errorf("more than one export target (%q and %q)", e.Target, a)
			}
			e.Target = a
			continue
		case a == "--exclude-imports":
			// The default of this translator: a dependency takes what it
			// names and nothing else. Nothing to carry over.
			continue
		case a == "--include-imports":
			return e, fmt.Errorf("--include-imports is not translated: it widens the exported set beyond what --path names")
		}
		for _, name := range []string{"--path", "--output", "-o"} {
			v, ok, err := value(name)
			if err != nil {
				return e, err
			}
			if !ok {
				continue
			}
			switch name {
			case "--path":
				e.Paths = append(e.Paths, path.Clean(v))
			default:
				e.Output = path.Clean(v)
			}
		}
		if strings.HasPrefix(a, "-") && a != "--exclude-imports" && !consumed(a) {
			return e, fmt.Errorf("unknown flag %s", a)
		}
	}
	if e.Target == "" {
		return e, fmt.Errorf("no export target")
	}
	if e.Output == "" {
		return e, fmt.Errorf("no --output: the vendored tree this fills cannot be identified")
	}
	if strings.HasPrefix(e.Target, registryHost) {
		e.Registry = true
		return e, nil
	}
	if err := e.parseTarget(); err != nil {
		return e, err
	}
	return e, nil
}

// consumed reports whether a is a flag parseExport handles.
func consumed(a string) bool {
	for _, name := range []string{"--path", "--output", "-o"} {
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}

// parseTarget splits a git target into its address and its fragment options.
func (e *bufExport) parseTarget() error {
	addr, frag, _ := strings.Cut(e.Target, "#")
	if addr == "" {
		return fmt.Errorf("empty repository address")
	}
	e.Git = addr
	if frag == "" {
		return nil
	}
	for _, kv := range strings.Split(frag, ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("fragment option %q is not key=value", kv)
		}
		switch k {
		case "subdir":
			e.Subdir = path.Clean(v)
		case "ref", "tag":
			e.Ref = v
		case "branch":
			return fmt.Errorf("fragment option branch=%s pins nothing; pin a commit instead", v)
		default:
			return fmt.Errorf("unknown fragment option %s", k)
		}
	}
	return nil
}

// exportInvocation and installInvocation are the words that make a line worth
// reading. They are named here because a define body is refused when it holds
// one of them, and that refusal has to look for exactly what the readers do.
const (
	exportInvocation  = "buf export"
	installInvocation = "go install"
)

// makeRecipePrefix is the character that marks a recipe line. .RECIPEPREFIX
// can change it; a Makefile that does is refused rather than read with every
// recipe mistaken for a make line.
const makeRecipePrefix = "\t"

// logicalLines joins backslash continuations and removes comments, returning
// one string per logical line.
//
// Fidelity, and its limits. This is not a Make parser and deliberately is not
// one: a second implementation of Make's expander would be its own source of
// bugs. It models exactly the two comment rules that decide whether a line is
// an invocation, both checked against GNU Make 4.4.1 rather than assumed:
//
//   - Outside a recipe, `#` starts a make comment even inside quotes — Make
//     has no quoting at this level (`V = 'a#b'` really does leave `V = 'a`).
//     `\#` is a literal hash. The comment runs to the end of the *logical*
//     line: a backslash at the end of a comment line continues the comment,
//     so the line after it is swallowed too.
//   - Inside a recipe (a line beginning with a tab), the text goes to the
//     shell, so `#` is a *shell* comment: it starts one only at a word
//     boundary and only outside quotes — `--path 'example/a#b'` and the `#` of
//     `repo.git#subdir=api` are data — and it ends at the newline, because
//     in the shell a backslash inside a comment continues nothing.
//
// Beyond that it does not go, and what it cannot model it refuses:
// .RECIPEPREFIX is an error, because it moves the boundary the whole model
// rests on, and a define/endef body is not read at all: what a body means is
// decided where the variable is expanded, which this reader does not follow.
// A body is dropped, and refused outright when it holds an invocation, since
// that is the only case where dropping it could lose one. Known limit, stated
// rather than guessed at: a tab-indented line outside any rule is read as a
// recipe.
func logicalLines(s string) ([]string, error) {
	physical := strings.Split(s, "\n")
	for _, line := range physical {
		if strings.HasPrefix(line, ".RECIPEPREFIX") {
			return nil, fmt.Errorf(".RECIPEPREFIX changes which lines are recipes; this reader understands only the default tab")
		}
	}
	physical, err := withoutDefineBodies(physical)
	if err != nil {
		return nil, err
	}
	var (
		out    []string
		cur    strings.Builder
		recipe bool
		open   bool
	)
	end := func() {
		text := cur.String()
		cur.Reset()
		open = false
		if recipe {
			text = strings.ReplaceAll(stripShellComments(text), "\n", " ")
		} else {
			text = stripMakeComment(text)
		}
		out = append(out, text)
	}
	for _, line := range physical {
		if !open {
			recipe = strings.HasPrefix(line, makeRecipePrefix)
			open = true
		}
		t := strings.TrimRight(line, " \t")
		if continued(t) {
			cur.WriteString(strings.TrimSuffix(t, `\`))
			// A recipe keeps its line boundary: a shell comment ends there.
			// Outside a recipe Make replaces the continuation with a space,
			// and a comment reaches across it.
			if recipe {
				cur.WriteString("\n")
			} else {
				cur.WriteString(" ")
			}
			continue
		}
		cur.WriteString(line)
		end()
	}
	if cur.Len() > 0 {
		end()
	}
	return out, nil
}

// withoutDefineBodies removes the body of every define/endef block, and
// refuses a body that holds an invocation.
//
// A define body is stored verbatim and means whatever the place it is
// expanded makes it mean. Verified against GNU Make 4.4.1: inside a body
// nothing is stripped — `echo one # two` keeps its hash and `a\#b` keeps its
// backslash — and the very same body becomes a recipe, read by the shell's
// rules, when it is expanded inside one. This reader does not follow
// expansion, so it cannot say which rules apply, and reading a body by its own
// indentation would answer that question by guessing.
//
// Dropping a body loses nothing this reader was going to recover from it,
// except when the body holds an invocation — and then it is refused by name.
// Refusing every define instead would refuse the many Makefiles that use one
// for something this reader never reads.
func withoutDefineBodies(physical []string) ([]string, error) {
	var (
		out   []string
		depth int
		start int
	)
	for n, line := range physical {
		switch {
		case isDirective(line, "define"):
			if depth == 0 {
				start = n + 1
			}
			depth++
		case isDirective(line, "endef"):
			if depth > 0 {
				depth--
			}
		case depth > 0:
			for _, word := range []string{exportInvocation, installInvocation} {
				if strings.Contains(line, word) {
					return nil, fmt.Errorf("line %d: `%s` inside a define body: what a body means is decided where the variable is expanded, which this reader does not follow; move the invocation out of the define, or translate it by hand", n+1, word)
				}
			}
		}
		if depth == 0 && !isDirective(line, "endef") {
			out = append(out, line)
			continue
		}
		// A dropped line still has to occupy its place, so that a line
		// number in a message means the line the author wrote.
		out = append(out, "")
	}
	if depth > 0 {
		return nil, fmt.Errorf("line %d: define with no endef; where the body ends decides which lines are make text, and nothing here says", start)
	}
	return out, nil
}

// isDirective reports whether a physical line opens or closes a define block.
// Make allows leading spaces before a directive but not a tab, which would
// make the line a recipe instead. The word has to be followed by whitespace or
// by nothing: `define=1` assigns a variable named define, as GNU Make 4.4.1
// confirms, and refusing that file for a missing `endef` it never needed would
// cost as much as an unresolved item that is not real.
func isDirective(line, word string) bool {
	rest, ok := strings.CutPrefix(strings.TrimLeft(line, " "), word)
	if !ok {
		return false
	}
	return rest == "" || strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "\t")
}

// continued reports whether a line ends in a continuation. An even number of
// trailing backslashes escapes itself and continues nothing.
func continued(t string) bool {
	n := 0
	for i := len(t) - 1; i >= 0 && t[i] == '\\'; i-- {
		n++
	}
	return n%2 == 1
}

// stripMakeComment removes a make comment from a logical line outside any
// recipe. Quotes do not protect a `#` here; a backslash does.
//
// What a backslash does exactly was measured against GNU Make 4.4.1, since
// its documentation is vague and no measured Makefile exercises it. In the run
// of backslashes immediately before a `#`, every pair collapses to one
// backslash; a leftover odd backslash then escapes the hash, and an even run
// leaves the hash to open a comment. `a\\#b` is therefore `a\` and a comment,
// while `a\\\#b` is `a\#b` entire. The collapsing is local to that run: a
// backslash with no hash after it is left exactly as written, so `a\\b` stays
// `a\\b`.
func stripMakeComment(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '#' {
			return b.String()
		}
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		n := 0
		for i+n < len(s) && s[i+n] == '\\' {
			n++
		}
		if i+n == len(s) || s[i+n] != '#' {
			b.WriteString(strings.Repeat(`\`, n))
			i += n - 1
			continue
		}
		b.WriteString(strings.Repeat(`\`, n/2))
		if n%2 == 0 {
			return b.String()
		}
		b.WriteByte('#')
		i += n
	}
	return b.String()
}

// stripShellComments removes the shell comments of a recipe. Newlines mark
// where one physical line ends; a comment ends with it.
func stripShellComments(s string) string {
	var (
		b     strings.Builder
		quote byte
		prev  byte = '\n' // the start of the text is a word boundary
	)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if quote == '"' && c == '\\' && i+1 < len(s) {
				b.WriteByte(c)
				i++
				b.WriteByte(s[i])
				prev = 'x'
				continue
			}
			b.WriteByte(c)
			if c == quote {
				quote = 0
			}
			prev = c
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			b.WriteByte(c)
		case '\\':
			b.WriteByte(c)
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			}
			prev = 'x'
			continue
		case '#':
			if !wordBoundary(prev) {
				b.WriteByte(c)
				break
			}
			for i < len(s) && s[i] != '\n' {
				i++
			}
			if i < len(s) {
				b.WriteByte('\n')
			}
			prev = '\n'
			continue
		default:
			b.WriteByte(c)
		}
		prev = c
	}
	return b.String()
}

// wordBoundary reports whether a `#` following c begins a word, which is
// where the shell starts a comment.
func wordBoundary(c byte) bool {
	switch c {
	case ' ', '\t', '\n', ';', '|', '&', '(':
		return true
	}
	return false
}

// fields splits a shell-ish command line, honouring single and double quotes.
// It is not a shell: an unterminated quote is an error rather than a guess.
func fields(line string) ([]string, error) {
	var (
		out   []string
		cur   strings.Builder
		quote rune
		open  bool
	)
	flush := func() {
		if open {
			out = append(out, cur.String())
			cur.Reset()
			open = false
		}
	}
	for _, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			open = true
		case r == ' ' || r == '\t':
			flush()
		default:
			open = true
			cur.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote", quote)
	}
	flush()
	return out, nil
}
