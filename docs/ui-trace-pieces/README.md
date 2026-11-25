# Trace pieces and Control room look

Screenshots of the UI with the bundled Trace piece set (`internal/web/static/pieces/`, drawn by
`scripts/gen_pieces.py`) and the shared Control room stylesheet (`internal/web/static/og.css`).
Captured with headless Chrome against `gambit serve --live --mock` (mock data, not a result).

- `piece-sheet.png`: all 12 pieces at 72, 40 and 24 px on light and dark squares
- `replay-blunder-*`: recorded game at a blunder (BLUNDER tag, dashed best-move frame, last-move marks)
- `replay-check-*`: a check (CHECK tag on the king)
- `human-hover-*`: human mode, with a move's from (blue) and to (orange) squares marked
- `tutorial-*`: tutorial pages using the same pieces and tags
- `knight-candidates.png`: the old L-shaped arrow knight (OLD) and the four trace-style knights drawn for the redesign: NA (shipped: two waterfall span bars stepping up into a horse-profile head), NB (service-graph chain of nodes), NC (parent span with a child span branching sideways) and ND (three staggered waterfall bars). To swap, move that design's shapes into the `"N"` entry of `scripts/gen_pieces.py` and rerun it.

The knight is the L jump drawn as a trace: two span bars stack up the neck (two along), then the head juts sideways (one across). Its pointed horse profile and eye node tell it apart from the bishop's straight diagonal at 24 px; white and black still differ by fill and outline, not hue.
