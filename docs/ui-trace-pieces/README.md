# Trace pieces and Control room look

Screenshots of the UI with the bundled Trace piece set (`internal/web/static/pieces/`, drawn by
`scripts/gen_pieces.py`) and the shared Control room stylesheet (`internal/web/static/og.css`).
Captured with headless Chrome against `gambit serve --live --mock` (mock data, not a result).

- `piece-sheet.png`: all 12 pieces at 72, 40 and 24 px on light and dark squares
- `replay-blunder-*`: recorded game at a blunder (BLUNDER tag, dashed best-move frame, last-move marks)
- `replay-check-*`: a check (CHECK tag on the king)
- `human-hover-*`: human mode, with a move's from (blue) and to (orange) squares marked
- `tutorial-*`: tutorial pages using the same pieces and tags
