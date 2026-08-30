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
//
// Two more things travel with a deleted entry rather than being left
// behind: the comment lines immediately above it (its yaml.Node.HeadComment
// — otherwise a permission's rationale would be left dangling above
// whatever entry a future edit adds under the same key, silently
// misdescribing it), and, when every entry of allow[] is pruned, the
// allow: key itself (a bare key with nothing under it is not what "nothing
// here" should look like, and it would swallow the very next comment
// written under it the same way).
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

	breakingKey, breakingNode := mapEntry(doc, "breaking")
	if breakingNode == nil {
		return fmt.Errorf("%s: has no breaking: block to prune", manifestPath)
	}
	allowKey, allowNode := mapEntry(breakingNode, "allow")
	if allowNode == nil || allowNode.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s: has no breaking.allow list to prune", manifestPath)
	}
	for _, i := range idx {
		if i < 0 || i >= len(allowNode.Content) {
			return fmt.Errorf("%s: allow[%d]: no such permission to prune", manifestPath, i)
		}
	}

	// allLines is every Line the whole document's node tree carries,
	// sorted and de-duplicated. It is how the end of each deleted block is
	// found: the boundary is the next line anywhere in the document that
	// does not belong to the block's own subtree, whatever key or nesting
	// happens to come after it — the allow list's own next entry, a
	// sibling key of breaking, a following top-level key, or nothing, in
	// which case the block runs to the end of the file.
	//
	// headComments maps that same boundary line to how many comment lines
	// sit directly above it and belong to it (its own HeadComment), so a
	// deletion never eats into a line a surviving node owns: the boundary
	// is drawn before that node's comment, not after it.
	var allLines []int
	collectLines(doc, &allLines)
	sort.Ints(allLines)
	headComments := make(map[int]int)
	collectHeadComments(doc, headComments)

	totalLines := realLineCount(raw)
	toDelete := make([]bool, totalLines+2) // 1-indexed; index 0 unused

	// deleteBlock marks [startNode.Line - (startNode's own head-comment
	// lines), boundary] for deletion, where boundary stops short of
	// whatever node — deleted or not — the document places next, and short
	// again of that node's own head comment when it has one.
	deleteBlock := func(startNode, endNode *yaml.Node) {
		start := startNode.Line - commentLines(startNode.HeadComment)
		mx := maxLine(endNode)
		end := totalLines
		for _, l := range allLines {
			if l > mx {
				end = l - 1 - headComments[l]
				break
			}
		}
		for l := start; l <= end && l <= totalLines; l++ {
			if l >= 1 {
				toDelete[l] = true
			}
		}
	}

	if len(idx) == len(allowNode.Content) {
		// Every entry is going: the key itself goes rather than being left
		// bare. If allow was the only thing this breaking: block carried,
		// "same for any other key this leaves empty" applies one level up
		// too.
		deleteBlock(allowKey, allowNode)
		if len(breakingNode.Content) == 2 {
			deleteBlock(breakingKey, breakingNode)
		}
	} else {
		for _, i := range idx {
			item := allowNode.Content[i]
			deleteBlock(item, item)
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

// mapEntry returns the key and value nodes for key in mapping node m, or
// (nil, nil) if m is not a mapping or carries no such key.
func mapEntry(m *yaml.Node, key string) (k, v *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
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

// collectHeadComments records, for every node in n's subtree that carries a
// HeadComment, how many lines that comment spans, keyed by the node's own
// Line — the boundary line a deletion ending just before it must stop
// short of.
func collectHeadComments(n *yaml.Node, out map[int]int) {
	if n == nil {
		return
	}
	if c := commentLines(n.HeadComment); c > 0 {
		if c > out[n.Line] {
			out[n.Line] = c
		}
	}
	for _, c := range n.Content {
		collectHeadComments(c, out)
	}
}

// commentLines returns how many source lines a yaml.Node.HeadComment
// occupies: one per "\n"-separated line, zero for an empty comment.
func commentLines(c string) int {
	if c == "" {
		return 0
	}
	return strings.Count(c, "\n") + 1
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
