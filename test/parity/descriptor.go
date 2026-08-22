//go:build parity

package parity

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// describeDifference renders why two generated files differ, in the terms the
// difference is actually about.
//
// Most of what determines the bytes of a .pb.go file is not in the Go source
// at all: it is in the serialised FileDescriptorProto the file embeds. A
// difference in a file option, in the order of proto_file, or in whether
// source info survived shows up in the Go text as one unreadable string
// literal against another. Diffing that as text says "these bytes differ",
// which is exactly the amount of information that gets a parity failure
// ignored. So when both sides carry a descriptor, the descriptor is decoded
// and compared as text, and the Go source is only the fallback.
func describeDifference(want, got []byte) string {
	dw, errW := rawDescriptor(want)
	dg, errG := rawDescriptor(got)
	if errW == nil && errG == nil {
		if d := textDiff(prototext.Format(dw), prototext.Format(dg)); d != "" {
			return "the embedded descriptors differ\n" + d
		}
		// Equal descriptors and unequal files means the generator wrote
		// different code from the same descriptor — the request differed in
		// something outside it, such as what else was in file_to_generate.
		return "the embedded descriptors are equal, so the difference is in the generated code itself\n" +
			textDiff(string(want), string(got))
	}
	return textDiff(string(want), string(got))
}

// rawDescriptor decodes the FileDescriptorProto a generated Go file embeds.
//
// The generator emits it as a const named <mangled path>_rawDesc, a
// concatenation of string literals. Nothing else in the file is parsed: the
// point is to reach the descriptor, not to understand Go.
func rawDescriptor(src []byte) (*descriptorpb.FileDescriptorProto, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "generated.go", src, 0)
	if err != nil {
		return nil, err
	}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
				continue
			}
			if !strings.HasSuffix(vs.Names[0].Name, "_rawDesc") {
				continue
			}
			s, err := stringExpr(vs.Values[0])
			if err != nil {
				return nil, err
			}
			var fd descriptorpb.FileDescriptorProto
			if err := proto.Unmarshal([]byte(s), &fd); err != nil {
				return nil, fmt.Errorf("decoding the embedded descriptor: %w", err)
			}
			return &fd, nil
		}
	}
	return nil, fmt.Errorf("this file embeds no descriptor")
}

// stringExpr evaluates a constant expression made of string literals joined
// with +, which is the only shape the generator emits.
func stringExpr(e ast.Expr) (string, error) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", fmt.Errorf("not a string literal: %s", v.Value)
		}
		return strconv.Unquote(v.Value)
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", fmt.Errorf("unexpected operator %s", v.Op)
		}
		l, err := stringExpr(v.X)
		if err != nil {
			return "", err
		}
		r, err := stringExpr(v.Y)
		if err != nil {
			return "", err
		}
		return l + r, nil
	case *ast.ParenExpr:
		return stringExpr(v.X)
	default:
		return "", fmt.Errorf("unexpected expression %T", e)
	}
}

// textDiff renders the differing lines of two texts with a little context,
// prefixed the way a diff is, and truncated: a parity failure is read to find
// the first cause, and a thousand lines of it hides that.
func textDiff(want, got string) string {
	wl := strings.Split(want, "\n")
	gl := strings.Split(got, "\n")
	first := -1
	for i := 0; i < len(wl) || i < len(gl); i++ {
		if at(wl, i) != at(gl, i) {
			first = i
			break
		}
	}
	if first < 0 {
		return ""
	}
	const window = 20
	var b strings.Builder
	fmt.Fprintf(&b, "  first difference at line %d\n", first+1)
	for i := max(0, first-resyncContext); i < first; i++ {
		fmt.Fprintf(&b, "    %s\n", at(wl, i))
	}
	// A pure insertion or deletion would otherwise mis-align every remaining
	// line and bury the one line that matters under a screenful of noise, so
	// the two sides are re-synchronised before anything is printed.
	dw, dg := resync(wl, gl, first, window)
	for i := first; i < first+dw; i++ {
		fmt.Fprintf(&b, "  - %s\n", at(wl, i))
	}
	for i := first; i < first+dg; i++ {
		fmt.Fprintf(&b, "  + %s\n", at(gl, i))
	}
	for k := 0; k < resyncContext; k++ {
		fmt.Fprintf(&b, "    %s\n", at(wl, first+dw+k))
	}
	return b.String()
}

// resync finds the shortest pair of runs, one from each side, whose removal
// puts the two texts back in step at the first line after them. It reports how
// many lines of each side belong to the difference; when nothing re-syncs
// inside the window it falls back to reporting the window itself, which is the
// honest answer for two texts that simply diverge.
func resync(wl, gl []string, first, window int) (int, int) {
	for total := 1; total <= 2*window; total++ {
		for dw := max(0, total-window); dw <= min(total, window); dw++ {
			dg := total - dw
			if dw == 0 && dg == 0 {
				continue
			}
			if aligned(wl, gl, first+dw, first+dg, resyncContext) {
				return dw, dg
			}
		}
	}
	return window, window
}

// resyncContext is how many lines have to agree before two texts count as back in
// step. One would re-sync on a stray brace.
const resyncContext = 3

// aligned reports whether the next n lines of the two sides agree.
func aligned(wl, gl []string, i, j, n int) bool {
	if i >= len(wl)+n && j >= len(gl)+n {
		return false
	}
	for k := 0; k < n; k++ {
		if at(wl, i+k) != at(gl, j+k) {
			return false
		}
	}
	return true
}

func at(lines []string, i int) string {
	if i < 0 || i >= len(lines) {
		return ""
	}
	return lines[i]
}
