// Package protoscan reads the import statements of a .proto file without
// compiling it.
//
// It is here rather than beside either of its callers because both of them
// need the same answer before a compiler can be run at all: resolution decides
// which files a narrowed dependency still has to offer, and migration works
// out which third-party files a repository actually reads. Two scanners over
// one grammar would be the second copy of a fact — and the first thing they
// would disagree about is a commented-out import, which neither author would
// think to look at.
package protoscan

import (
	"bufio"
	"bytes"
	"strings"
)

// Imports reads the import statements of one .proto.
//
// It is a scanner, not a parser, and the difference is deliberate: linking the
// file would mean resolving it, which is the thing that cannot be done yet —
// the dependencies this reads the imports to discover are exactly what a
// resolver would need. What it models is the one statement it cares about:
// `import`, optionally `public` or `weak`, then a quoted path. Comments are
// stripped first so that a commented-out import is not counted, and a string
// inside a comment marker inside a string is left alone.
func Imports(b []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	inBlockComment := false
	for sc.Scan() {
		line, _ := stripProtoComments(sc.Text(), &inBlockComment)
		rest := strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(rest, "import")
		if !ok || (rest != "" && !isSpace(rest[0])) {
			continue
		}
		rest = strings.TrimSpace(rest)
		for _, kw := range []string{"public", "weak"} {
			if r, ok := strings.CutPrefix(rest, kw); ok && r != "" && isSpace(r[0]) {
				rest = strings.TrimSpace(r)
			}
		}
		if !strings.HasPrefix(rest, `"`) {
			continue
		}
		if end := strings.IndexByte(rest[1:], '"'); end >= 0 {
			out = append(out, rest[1:1+end])
		}
	}
	return out
}

// stripProtoComments removes // and /* */ comments from one line, tracking an
// open block comment across lines. Quoted strings are honoured so that a path
// containing the comment markers survives.
func stripProtoComments(line string, inBlock *bool) (string, bool) {
	var b strings.Builder
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case *inBlock:
			if c == '*' && i+1 < len(line) && line[i+1] == '/' {
				*inBlock = false
				i++
			}
		case quote != 0:
			b.WriteByte(c)
			if c == '\\' && i+1 < len(line) {
				i++
				b.WriteByte(line[i])
				continue
			}
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
			b.WriteByte(c)
		case c == '/' && i+1 < len(line) && line[i+1] == '/':
			return b.String(), true
		case c == '/' && i+1 < len(line) && line[i+1] == '*':
			*inBlock = true
			i++
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), false
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' }
