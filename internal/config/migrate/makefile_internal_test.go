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
