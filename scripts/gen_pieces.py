#!/usr/bin/env python3
"""Generates the 12 "Trace pieces" SVGs in internal/web/static/pieces/.

Original artwork, drawn from scratch: every piece is built from observability shapes.
  king   = root-span stack topped by a cross       queen  = service-graph hub with five radiating nodes
  rook   = stacked metric tower                    bishop = diagonal trace line with a diamond head
  knight = L-shaped jump arrow                     pawn   = a single dot-span
Sides are told apart without colour: white is a pale fill with a dark outline and cyan bars,
black is a dark fill with a light halo and amber bars.

Usage: python3 scripts/gen_pieces.py [outdir]   (default: internal/web/static/pieces)
"""
import os, sys

out = sys.argv[1] if len(sys.argv) > 1 else os.path.join(os.path.dirname(__file__), "..", "internal", "web", "static", "pieces")
W = dict(fill="#eefbff", line="#06141c", halo="#06141c", acc="#0a8fa1")  # white: light fill, dark outline, cyan bars
B = dict(fill="#1b2230", line="#06141c", halo="#f4f7fb", acc="#f5a623")  # black: dark fill, light halo, amber bars


def shp(s, st):
    k = s[0]
    if k == "r": _, x, y, w, h, r = s; return f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{r}" {st}/>'
    if k == "c": _, x, y, r = s; return f'<circle cx="{x}" cy="{y}" r="{r}" {st}/>'
    if k == "p": return f'<polygon points="{s[1]}" {st}/>'
    if k == "d": return f'<path d="{s[1]}" {st}/>'
    if k == "l": _, x1, y1, x2, y2, w = s; return f'<line x1="{x1}" y1="{y1}" x2="{x2}" y2="{y2}" stroke-linecap="round" {st}/>'


def render(shapes, bars, col):
    """shapes: filled, outlined forms; bars: accent dividers drawn on top."""
    halo, main = [], []
    for s in shapes:
        if s[0] == "l":  # a line (a span link): wide halo, dark outline, then the fill colour
            w = s[5]
            halo.append(shp(s, f'stroke="{col["halo"]}" stroke-width="{w + 9}"'))
            main.append(shp(s, f'stroke="{col["line"]}" stroke-width="{w + 3.5}"'))
            main.append(shp(s, f'stroke="{col["fill"]}" stroke-width="{w - 1}"'))
        else:
            halo.append(shp(s, f'fill="{col["halo"]}" stroke="{col["halo"]}" stroke-width="9" stroke-linejoin="round"'))
            main.append(shp(s, f'fill="{col["fill"]}" stroke="{col["line"]}" stroke-width="3.5" stroke-linejoin="round"'))
    acc = "".join(shp(s, f'fill="{col["acc"]}" stroke="none"') for s in bars)
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100" width="100" height="100">'
            f'{"".join(halo)}{"".join(main)}{acc}</svg>\n')


BASE = ("r", 20, 84, 60, 9, 4.5)
T = {
    "P": ([("c", 50, 44, 17), BASE, ("r", 34, 68, 32, 12, 5)], [("r", 38, 72, 24, 4, 2)]),
    "R": ([("p", "27,22 27,34 35,34 35,26 44,26 44,34 56,34 56,26 65,26 65,34 73,34 73,22"), ("r", 30, 34, 40, 46, 3), BASE],
          [("r", 34, 42, 32, 6, 3), ("r", 34, 54, 32, 6, 3), ("r", 34, 66, 32, 6, 3)]),
    "B": ([("p", "50,8 62,22 50,36 38,22"), ("p", "42,36 58,36 70,78 30,78"), ("r", 24, 78, 52, 8, 4), BASE],
          [("p", "34,56 66,38 66,45 34,63")]),
    "N": ([("d", "M30 82 L30 46 Q30 22 54 22 L66 22 L66 12 L90 32 L66 52 L66 42 L54 42 L54 82 Z"), BASE, ("c", 44, 31, 0.1)],
          [("c", 43, 34, 4.5)]),
    # Queen: a wide service-graph hub. Five separate nodes on an arc, each tied to the hub by a span link,
    # so at 24 px it reads as a spiky crown that is clearly wider than the king's narrow tower.
    "Q": ([("l", 50, 54, 12, 30, 5), ("l", 50, 54, 31, 15, 5), ("l", 50, 54, 50, 9, 5), ("l", 50, 54, 69, 15, 5), ("l", 50, 54, 88, 30, 5),
           ("p", "34,50 66,50 78,80 22,80"), ("c", 50, 54, 14),
           ("c", 12, 30, 7.5), ("c", 31, 15, 7.5), ("c", 50, 9, 7.5), ("c", 69, 15, 7.5), ("c", 88, 30, 7.5), BASE],
          [("c", 50, 54, 6)]),
    # King: a narrow, tall root-span stack with a bold cross that rises above everything else.
    "K": ([("r", 44, 2, 12, 30, 3), ("r", 33, 10, 34, 11, 3), ("r", 30, 32, 40, 12, 4), ("r", 34, 45, 32, 11, 4), ("r", 38, 58, 28, 11, 4), ("r", 30, 70, 40, 12, 4), BASE],
          [("r", 34, 36, 32, 4, 2), ("r", 38, 49, 24, 4, 2), ("r", 42, 62, 16, 4, 2), ("r", 34, 74, 32, 4, 2)]),
}
os.makedirs(out, exist_ok=True)
for k, (sh, br) in T.items():
    for side, col in (("w", W), ("b", B)):
        with open(os.path.join(out, f"{side}{k}.svg"), "w") as f:
            f.write(render(sh, br, col))
print("wrote 12 pieces to", os.path.normpath(out))
