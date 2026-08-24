package migrate

import "testing"

// TestBothReadersRefuseTheSameConditionTheSameWay: .RECIPEPREFIX moves the
// boundary the whole comment model rests on, so neither reader can read the
// file. parseExports says so by returning an error. parseInstalls used to put
// the same sentence in its list of unreadable invocations, where the caller
// turns it into an unresolved item — and the caller then aborts on the error
// parseExports returns, so that item is never printed. A refusal a human never
// sees is not a refusal.
func TestBothReadersRefuseTheSameConditionTheSameWay(t *testing.T) {
	const src = ".RECIPEPREFIX = >\n>go install example.test/cmd/tool@v1.0.0\n"

	_, _, exportErr := parseExports([]byte(src))
	if exportErr == nil {
		t.Fatal("parseExports accepted .RECIPEPREFIX")
	}
	_, unparsed, installErr := parseInstalls([]byte(src))
	if installErr == nil {
		t.Errorf("parseInstalls returned no error, want %v", exportErr)
	}
	if len(unparsed) != 0 {
		t.Errorf("refusal swallowed into the unreadable list: %v", unparsed)
	}
}

// TestBackslashRunBeforeAHash records what GNU Make 4.4.1 does, measured
// rather than read out of its documentation, which is vague here.
//
// The probe was a Makefile of assignments printed back with $(info):
//
//	A = a\#b        $(info A=[$(A)])   ->  A=[a#b]
//	B = a\\#b                          ->  B=[a\]
//	C = a\\\#b                         ->  C=[a\#b]
//	D = a\\\\#b                        ->  D=[a\\]
//	E = a\\\\\#b                       ->  E=[a\\#b]
//
// So Make counts the run of backslashes that immediately precedes a `#`:
// every pair in it collapses to one backslash, and a leftover odd backslash
// escapes the hash. An even run leaves the hash to open a comment. A second
// probe showed the collapsing is local to that run — `F = a\\b` stays `a\\b`,
// with no hash in sight — so a backslash elsewhere is left exactly as written.
//
// A two-character lookahead cannot express this: it reads `a\\#b` as a kept
// `\\` and then a comment, which is one backslash too many, and `a\\\#b` as
// `a\\#b`, which is a hash Make would have escaped only after collapsing.
func TestBackslashRunBeforeAHash(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`a\#b`, `a#b`},
		{`a\\#b`, `a\`},
		{`a\\\#b`, `a\#b`},
		{`a\\\\#b`, `a\\`},
		{`a\\\\\#b`, `a\\#b`},
		{`a\\b`, `a\\b`},
		{`a#b`, `a`},
		{`a\`, `a\`},
	} {
		if got := stripMakeComment(c.in); got != c.want {
			t.Errorf("stripMakeComment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
