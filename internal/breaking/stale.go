package breaking

import (
	"fmt"
	"strings"

	"github.com/thegorangers/stele/internal/config"
)

// StaleMoves reports the moves whose source names nothing in the previous
// revision. The rename has passed out of the comparison window: prev has
// moved on (the renamed declaration is gone, or was never there at this
// point in history), so rewriting it through this entry no longer does
// anything, and the entry can go.
//
// This deliberately looks at prev, not cur. ValidateMoves' checks on a move
// — that the source has not gone AND come back, that the destination is
// owned — are both about cur, because they ask whether a move still
// describes something true about the current revision. Staleness asks a
// different question: whether the OLD name is still there to be rewritten
// at all. A helper that answered both from the same revision would blur
// them, and the two are not interchangeable — see ValidateMoves' own doc
// comment for why that check runs against cur.
func StaleMoves(prev Revision, moves []config.Move) []config.Move {
	pkgs := map[string]bool{}
	paths := map[string]bool{}
	owned := map[string]bool{}
	for _, p := range prev.Owned {
		owned[p] = true
	}
	for _, fd := range prev.Files {
		if !owned[fd.Path()] {
			continue
		}
		pkgs[string(fd.Package())] = true
		paths[fd.Path()] = true
	}

	var stale []config.Move
	for _, m := range moves {
		if strings.HasPrefix(m.From, config.MoveFilePrefix) {
			if !paths[strings.TrimPrefix(m.From, config.MoveFilePrefix)] {
				stale = append(stale, m)
			}
			continue
		}
		if !pkgs[m.From] {
			stale = append(stale, m)
		}
	}
	return stale
}

// MoveNotes renders one line per stale move, in the voice PermitNotes uses
// for a stale permission: name what was declared, say why it no longer
// does anything, and say it can be removed.
func MoveNotes(stale []config.Move) []string {
	if len(stale) == 0 {
		return nil
	}
	notes := make([]string, 0, len(stale))
	for _, m := range stale {
		notes = append(notes, fmt.Sprintf(
			"stele: breaking: the move from %s to %s is stale: %s names nothing in the previous "+
				"revision, so the rename is behind the base and the entry can be removed",
			m.From, m.To, m.From))
	}
	return notes
}
