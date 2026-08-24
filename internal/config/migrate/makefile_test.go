package migrate_test

import (
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/config/migrate"
)

// realMakefileHeader is the head of the Makefile of a measured repository as it stood
// before that repository was migrated. The prose comment mentions `buf export`
// and, being prose, contains an apostrophe ("each service's"). Read as an
// invocation it is an unterminated quote; read as what it is, it is a sentence.
const realMakefileHeader = "PROTO_DST := third_party/proto\n" +
	"\n" +
	"# Where the service contracts are pulled from. `buf export` reads each service's\n" +
	"# api/ straight from its git repo (same mechanism the services use for their own\n" +
	"# cross-service protos) — no sibling checkout, always the services' current api/.\n" +
	"GIT_BASE ?= ssh://git@git.example.test/acme/services\n" +
	"\n" +
	"# Pull the api/ contracts of each dialled service via `buf export` (only the\n" +
	"# example/<svc> tree, imports excluded — cross-service imports come from the other\n" +
	"# exports), plus the well-known google + protovalidate protos from the public BSR.\n" +
	"vendor-proto:\n" +
	"\tbuf export \"$(GIT_BASE)/cart.git#subdir=api\" --exclude-imports --path example/cart --output=$(PROTO_DST)\n"

const genForVendoredTree = `
version: v2
plugins: [{local: protoc-gen-go, out: gen}]
inputs:
  - directory: third_party/proto
    paths: [third_party/proto/example/cart]
`

// TestCommentMentioningAnExportIsNotAnInvocation: migrate exits non-zero and
// prints what it could not translate so that a human reads the list. An entry
// that is not real teaches the reader to skim, which costs more than the entry
// it saves.
func TestCommentMentioningAnExportIsNotAnInvocation(t *testing.T) {
	r := mustMigrate(t, "version: v2\nmodules: [{path: third_party/proto}]\n", genForVendoredTree, realMakefileHeader)
	for _, u := range r.Unresolved {
		if strings.Contains(u, "could not translate an export invocation") {
			t.Errorf("comment reported as an invocation: %s", u)
		}
	}
}

// exportsOf runs the Makefile through the translator and returns what it could
// not translate, together with the dependency names it recovered.
func exportsOf(t *testing.T, makefile string) (deps []string, unresolved []string) {
	t.Helper()
	r := mustMigrate(t, "version: v2\nmodules: [{path: third_party/proto}]\n", genForVendoredTree, makefile)
	for _, d := range r.File.Deps {
		deps = append(deps, d.Name)
	}
	return deps, r.Unresolved
}

// TestHashInsideQuotesIsNotAShellComment: in a recipe the text goes to the
// shell, so a `#` inside quotes — and the `#` that opens a git fragment — is
// data. Reading either as a comment loses a real invocation silently.
func TestHashInsideQuotesIsNotAShellComment(t *testing.T) {
	deps, unresolved := exportsOf(t, "vendor-proto:\n"+
		"\tbuf export \"https://example.test/example/cart.git#subdir=api,ref=abc\" \\\n"+
		"\t\t--path 'example/cart#odd' --path example/cart --output=third_party/proto # vendored\n")
	if len(deps) != 1 || deps[0] != "cart" {
		t.Fatalf("deps = %v, want [cart]; unresolved = %v", deps, unresolved)
	}
}

// TestTrailingShellCommentDoesNotSwallowTheNextLine: in the shell a backslash
// inside a comment continues nothing, so a commented line ends at its newline.
// GNU Make hands the recipe to the shell verbatim, so the shell's rule is the
// one that applies.
func TestTrailingShellCommentDoesNotSwallowTheNextLine(t *testing.T) {
	deps, unresolved := exportsOf(t, "vendor-proto:\n"+
		"\techo hello # like this \\\n"+
		"\tbuf export \"https://example.test/example/cart.git#subdir=api,ref=abc\" --path example/cart --output=third_party/proto\n")
	if len(deps) != 1 || deps[0] != "cart" {
		t.Fatalf("deps = %v, want [cart]; unresolved = %v", deps, unresolved)
	}
}

// TestMakeCommentReachesAcrossItsOwnContinuation is the mirror image: outside a
// recipe a backslash at the end of a comment line continues the comment, so the
// line after it is comment too. Verified against GNU Make 4.4.1.
func TestMakeCommentReachesAcrossItsOwnContinuation(t *testing.T) {
	_, unresolved := exportsOf(t, "# a note about `buf export` that runs on \\\n"+
		"buf export 'not/an/invocation\n")
	for _, u := range unresolved {
		if strings.Contains(u, "could not translate an export invocation") {
			t.Errorf("continued comment reported as an invocation: %s", u)
		}
	}
}

// TestRecipePrefixIsRefused: .RECIPEPREFIX moves the boundary between a make
// comment and a shell comment, which is the whole of the model above. Refusing
// is the only honest answer; reading on would mis-parse in silence.
func TestRecipePrefixIsRefused(t *testing.T) {
	_, err := migrate.FromBuf(nil, []byte(genForVendoredTree), []byte(".RECIPEPREFIX = >\n>buf export x --output=third_party/proto\n"))
	if err == nil || !strings.Contains(err.Error(), ".RECIPEPREFIX") {
		t.Fatalf("err = %v, want a refusal naming .RECIPEPREFIX", err)
	}
}

// TestGenuineFailureStillReported guards the risk of this fix: silencing a
// real report is worse than printing a false one. An invocation that truly
// cannot be translated must survive comment handling.
func TestGenuineFailureStillReported(t *testing.T) {
	_, unresolved := exportsOf(t, "vendor-proto:\n"+
		"\tbuf export \"https://example.test/example/cart.git#branch=master\" --path example/cart --output=third_party/proto\n")
	var found bool
	for _, u := range unresolved {
		if strings.Contains(u, "branch=master pins nothing") {
			found = true
		}
	}
	if !found {
		t.Fatalf("real failure lost; unresolved = %v", unresolved)
	}
}

// TestDefineBodyHoldingAnInvocationIsRefused: a define body is stored
// verbatim and its meaning is decided where the variable is expanded, which
// this reader does not follow. Verified against GNU Make 4.4.1: inside a
// define body nothing is stripped — `echo one # two` keeps its hash, and so
// does `a\#b` — and the same body becomes a recipe when it is expanded in
// one. Reading such a body by its own indentation can therefore recover an
// invocation that never runs, or lose part of one that does, without saying
// so. Every other limit of this reader fails loudly; this one must too.
func TestDefineBodyHoldingAnInvocationIsRefused(t *testing.T) {
	_, err := migrate.FromBuf(nil, []byte(genForVendoredTree),
		[]byte("define vendor\n"+
			"buf export https://example.test/example/cart.git#subdir=api --output=third_party/proto\n"+
			"endef\n"))
	if err == nil || !strings.Contains(err.Error(), "define") {
		t.Fatalf("err = %v, want a refusal naming define", err)
	}
}

// TestDefineBodyWithoutAnInvocationIsNotRefused guards the cost of that
// refusal. define is ordinary in a Makefile; refusing every use of it would
// refuse files this reader translates correctly. Only a body that could carry
// an invocation is beyond it.
func TestDefineBodyWithoutAnInvocationIsNotRefused(t *testing.T) {
	deps, unresolved := exportsOf(t, "define forward\n"+
		"\techo 'starting' # a note\n"+
		"endef\n"+
		"vendor-proto:\n"+
		"\tbuf export \"https://example.test/example/cart.git#subdir=api,ref=abc\" --path example/cart --output=third_party/proto\n")
	if len(deps) != 1 || deps[0] != "cart" {
		t.Fatalf("deps = %v, want [cart]; unresolved = %v", deps, unresolved)
	}
}

// TestUnterminatedDefineIsRefused: without an endef there is no way to know
// where the body stops, so there is no way to know which lines were read as
// make text. GNU Make itself refuses this file; so does this reader.
func TestUnterminatedDefineIsRefused(t *testing.T) {
	_, err := migrate.FromBuf(nil, []byte(genForVendoredTree),
		[]byte("define vendor\necho hello\n"))
	if err == nil || !strings.Contains(err.Error(), "endef") {
		t.Fatalf("err = %v, want a refusal naming endef", err)
	}
}
