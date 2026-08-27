# Brand

The mark is a stele: an upright stone that carries an inscription. The stem of an
inverted Y cuts down through its crown and forks — one source above, divergence
below, which is what resolving a dependency graph looks like. The incision and the
inscribed lines share one stroke width, so the mark reads as cut by one chisel
rather than assembled from parts.

| file | use |
|---|---|
| `logo-light.svg` | on a light ground |
| `logo-dark.svg` | on a dark ground |
| `logo.svg` | takes `currentColor`; for HTML that sets a colour |
| `favicon.svg` | a self-contained tile, legible on a tab bar of any colour |

| `avatar-500.png` | the organisation avatar — GitHub takes PNG, GIF or JPG, not SVG |

The incision is a hole punched through the shape, not a light-coloured line painted
on top, so the mark carries no background of its own and sits on any ground.

## The avatar

`avatar.py` renders it; the PNG is committed so nobody has to run anything to get
it. It cannot share `logo.svg` — GitHub does not take SVG — so it is derived from
the same geometry rather than traced from the same file, and it differs in two
ways, both settled by downscaling to 40 px and looking rather than by argument:

- **The slab is light on an ink tile.** An avatar has no ground of its own to sit
  on; it brings one. Ink reads on GitHub's dark theme and its light one alike.
- **Two inscribed lines, not three.** At the size an avatar is actually seen — 40 px
  in a list — the third line merges into the second and the group becomes a smudge.
  The mark also sits larger in the frame for the same reason.

It stays inside the circle inscribed in the square, so it survives being cropped
round wherever that happens.

Below roughly 48 px the three inscribed lines stop resolving: at that size drop to
two, and below 24 px to one. `favicon.svg` already does this.

Clear space around the mark is at least the width of its own margin — 16 units of
the 80-unit width.
