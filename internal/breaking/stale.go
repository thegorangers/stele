package breaking

import (
	"fmt"
	"strings"

	"github.com/thegorangers/stele/internal/config"
)

// StaleMoves reports the moves whose source names nothing in the previous
// revision — nothing this repository owns, and nothing a dependency
// carries either. The rename has passed out of the comparison window: prev has
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
	pkgs, paths := revisionNames(prev, false)

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

// revisionNames returns the proto packages and the file paths rev carries.
// With ownedOnly set only this repository's own declarations are counted;
// otherwise a dependency's count too.
//
// Both readings are needed and they are not interchangeable. ValidateMoves
// asks whether a source was this repository's to rename, which only
// ownership can answer. Staleness asks the weaker question of whether the
// old name is present at all, and deliberately counts a dependency's names:
// a move pointed at a dependency is a mistake to be refused, not an entry
// to be quietly filed as stale and swept away by --prune.
func revisionNames(rev Revision, ownedOnly bool) (pkgs, paths map[string]bool) {
	pkgs = map[string]bool{}
	paths = map[string]bool{}
	owned := map[string]bool{}
	for _, p := range rev.Owned {
		owned[p] = true
	}
	for _, fd := range rev.Files {
		if ownedOnly && !owned[fd.Path()] {
			continue
		}
		pkgs[string(fd.Package())] = true
		paths[fd.Path()] = true
	}
	return pkgs, paths
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
