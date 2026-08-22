// Command extract prints the file options embedded in generated Go code.
//
// It exists because the option set a managed-mode generator synthesises is
// not documented anywhere we are willing to trust: it is a property of the
// generator that produced the artefact. So we read it off the artefact. Given
// a .pb.go, the command finds the embedded raw descriptor, decodes it as a
// FileDescriptorProto, and prints the file's name and its options as text.
//
// The output is meant to be captured as golden data for the synthesiser to
// reproduce.
//
// Usage:
//
//	go run ./internal/managed/extract <file.pb.go> [more.pb.go...]
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: extract <file.pb.go>...")
		os.Exit(2)
	}
	for _, path := range os.Args[1:] {
		if err := printOptions(os.Stdout, path); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			os.Exit(1)
		}
	}
}

// printOptions decodes one generated file and prints its descriptor's name
// and options.
func printOptions(w *os.File, path string) error {
	raw, err := rawDescriptor(path)
	if err != nil {
		return err
	}
	fd := &descriptorpb.FileDescriptorProto{}
	if err := proto.Unmarshal(raw, fd); err != nil {
		return fmt.Errorf("decode descriptor: %w", err)
	}
	opts := fd.GetOptions()
	if opts == nil {
		opts = &descriptorpb.FileOptions{}
	}
	text, err := prototext.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(opts)
	if err != nil {
		return fmt.Errorf("encode options: %w", err)
	}
	fmt.Fprintf(w, "# %s\n", fd.GetName())
	fmt.Fprintf(w, "# package %s\n", fd.GetPackage())
	fmt.Fprint(w, string(text))
	return nil
}

// rawDescriptor extracts the serialised FileDescriptorProto that generated Go
// code carries. Generated code stores it as a single declaration whose name
// ends in "rawDesc", built by concatenating string literals; the constant is
// read out of the syntax tree rather than by matching text, so that neither
// the concatenation layout nor the surrounding code has to be guessed at.
func rawDescriptor(path string) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	var found ast.Expr
	ast.Inspect(f, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range spec.Names {
			if !strings.HasSuffix(name.Name, "rawDesc") || i >= len(spec.Values) {
				continue
			}
			found = spec.Values[i]
			return false
		}
		return true
	})
	if found == nil {
		return nil, fmt.Errorf("no rawDesc declaration found")
	}
	s, err := stringValue(found)
	if err != nil {
		return nil, err
	}
	return []byte(s), nil
}

// stringValue evaluates an expression built only from string literals and the
// + operator. Anything else is reported rather than approximated: a raw
// descriptor read half-right is worse than one not read at all.
func stringValue(e ast.Expr) (string, error) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", fmt.Errorf("rawDesc literal is %s, want string", v.Kind)
		}
		return strconv.Unquote(v.Value)
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", fmt.Errorf("rawDesc uses operator %s, want +", v.Op)
		}
		x, err := stringValue(v.X)
		if err != nil {
			return "", err
		}
		y, err := stringValue(v.Y)
		if err != nil {
			return "", err
		}
		return x + y, nil
	case *ast.ParenExpr:
		return stringValue(v.X)
	default:
		return "", fmt.Errorf("rawDesc is not a constant string expression (%T)", e)
	}
}
