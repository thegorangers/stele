package protoscan_test

import (
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/protoscan"
)

// TestImports covers what the scanner has to get right for the two callers
// that depend on it: the shapes an import statement takes, and the shapes that
// only look like one.
func TestImports(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want []string
	}{
		{"plain", "syntax = \"proto3\";\nimport \"a/b.proto\";\n", []string{"a/b.proto"}},
		{"public and weak", "import public \"a.proto\";\nimport weak \"b.proto\";\n", []string{"a.proto", "b.proto"}},
		{"line comment", "// import \"a.proto\";\nimport \"b.proto\";\n", []string{"b.proto"}},
		{"block comment", "/*\nimport \"a.proto\";\n*/\nimport \"b.proto\";\n", []string{"b.proto"}},
		{"trailing comment", "import \"a.proto\"; // why\n", []string{"a.proto"}},
		{"not an import", "imported \"a.proto\";\noption go_package = \"b.proto\";\n", nil},
		{"indented", "  \timport \"a.proto\";\n", []string{"a.proto"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := protoscan.Imports([]byte(tc.body))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
