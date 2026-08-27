"""Render the organisation avatar. Run: python3 docs/brand/avatar.py

The avatar is a raster because GitHub takes PNG, GIF or JPG and not SVG, so it
cannot share logo.svg and is derived from the same geometry instead. It differs
from the mark in two deliberate ways, both measured by downscaling to 40 px and
looking: the slab is light on an ink tile so it reads on GitHub's dark theme,
and it carries two inscribed lines rather than three, because at the size an
avatar is actually seen the third line merges into the second.
"""
from PIL import Image, ImageDraw

INK, STONE = (28, 26, 23), (244, 241, 236)
SS = 8  # supersample, then downscale: the arc and the fork need it

def avatar(size, frac, cut_w, lines, out):
    S = size * SS
    im = Image.new('RGB', (S, S), INK)
    d = ImageDraw.Draw(im)
    mh = S * frac
    k = mh / 160.0
    ox = (S - 80 * k) / 2.0 - 20 * k
    oy = (S - mh) / 2.0
    P = lambda x, y: (ox + x * k, oy + y * k)

    d.rectangle([P(20, 40), P(100, 160)], fill=STONE)
    d.pieslice([P(20, 0), P(100, 80)], 180, 360, fill=STONE)

    w = int(round(cut_w * k))
    d.line([P(60, -8), P(60, 45)], fill=INK, width=w)
    d.line([P(41, 71), P(60, 45), P(79, 71)], fill=INK, width=w, joint='curve')
    for pt in (P(41, 71), P(79, 71), P(60, 45)):
        r = w / 2.0
        d.ellipse([pt[0]-r, pt[1]-r, pt[0]+r, pt[1]+r], fill=INK)

    for y, lw, h in lines:
        d.rounded_rectangle([P(36, y), P(36 + lw, y + h)], radius=h * k / 2.0, fill=INK)

    im.resize((size, size), Image.LANCZOS).save(out, 'PNG', optimize=True)
    return out

if __name__ == '__main__':
    print(avatar(500, 0.70, 11, [(100, 48, 11), (124, 28, 11)], 'docs/brand/avatar-500.png'))
