package breaking

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Prune deletes the entries of breaking.allow named by idx (indices into
// the same slice config.Load decoded, in the same order — the indices
// StaleAllowIndices returns) from the manifest at manifestPath, and leaves
// every other byte of the file exactly as it was.
//
// That last clause is the whole point and the reason this is line-range
// surgery rather than a re-emit. internal/config/migrate/emit.go generates
// a manifest from a struct with a fresh encoder; it never reads an existing
// file and does not round-trip, and re-emitting a parsed yaml.Node loses
// quoting style, blank lines and indentation — a diff nobody wrote and
// nobody can review. So Prune parses the raw file a second time, purely to
// read yaml.Node.Line off the nodes it is about to delete, and then removes
// exactly those source lines from the raw bytes. The struct config.Load
// decoded is never consulted here beyond the indices the caller already
// worked out from it.
func Prune(manifestPath string, idx []int) error {
	if len(idx) == 0 {
		return nil
	}

	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}

	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	if len(root.Content) == 0 {
		return fmt.Errorf("%s: empty document", manifestPath)
	}
	doc := root.Content[0]

	breakingNode := mapValue(doc, "breaking")
	if breakingNode == nil {
		return fmt.Errorf("%s: has no breaking: block to prune", manifestPath)
	}
	allowNode := mapValue(breakingNode, "allow")
	if allowNode == nil || allowNode.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s: has no breaking.allow list to prune", manifestPath)
	}

	// allLines is every Line the whole document's node tree carries,
	// sorted and de-duplicated. It is how the end of each pruned entry is
	// found: the boundary is the next line anywhere in the document that
	// does not belong to the entry's own subtree, whatever key or nesting
	// happens to come after it — the allow list's own next entry, a
	// sibling key of breaking, a following top-level key, or nothing, in
	// which case the entry runs to the end of the file.
	var allLines []int
	collectLines(doc, &allLines)
	sort.Ints(allLines)

	totalLines := realLineCount(raw)

	toDelete := make([]bool, totalLines+1) // 1-indexed; index 0 unused
	for _, i := range idx {
		if i < 0 || i >= len(allowNode.Content) {
			return fmt.Errorf("%s: allow[%d]: no such permission to prune", manifestPath, i)
		}
		item := allowNode.Content[i]
		start := item.Line
		end := totalLines
		mx := maxLine(item)
		for _, l := range allLines {
			if l > mx {
				end = l - 1
				break
			}
		}
		for l := start; l <= end && l <= totalLines; l++ {
			toDelete[l] = true
		}
	}

	lines := splitKeepingEnds(raw)
	var out strings.Builder
	for i, line := range lines {
		if toDelete[i+1] {
			continue
		}
		out.WriteString(line)
	}

	info, err := os.Stat(manifestPath)
	if err != nil {
		return err
	}
	return os.WriteFile(manifestPath, []byte(out.String()), info.Mode())
}

// mapValue returns the value node for key in mapping node m, or nil if m is
// not a mapping or carries no such key.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// collectLines appends the Line of n and of every node in its subtree.
func collectLines(n *yaml.Node, out *[]int) {
	if n == nil {
		return
	}
	*out = append(*out, n.Line)
	for _, c := range n.Content {
		collectLines(c, out)
	}
}

// maxLine returns the greatest Line touched anywhere in n's subtree —
// where n's own text ends, as far as the parse tree records it.
func maxLine(n *yaml.Node) int {
	mx := n.Line
	for _, c := range n.Content {
		if l := maxLine(c); l > mx {
			mx = l
		}
	}
	return mx
}

// splitKeepingEnds splits raw into lines, each element carrying its own
// trailing "\n" (the final element does not, if raw does not end in one).
// Joining the result back together reproduces raw exactly.
func splitKeepingEnds(raw []byte) []string {
	s := string(raw)
	if s == "" {
		return nil
	}
	parts := strings.SplitAfter(s, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// realLineCount is len(splitKeepingEnds(raw)): the number of actual lines
// of text raw carries, whether or not the last one ends in a newline.
func realLineCount(raw []byte) int {
	return len(splitKeepingEnds(raw))
}
