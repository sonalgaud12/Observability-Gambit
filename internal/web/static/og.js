// Shared helpers for the three pages: the Trace piece set (bundled SVGs in pieces/, no CDN)
// and the check marker. Loaded before each page's own script.
const OG = (() => {
  const KINDS = ["k", "q", "r", "b", "n", "p"];
  const NAME = { k: "king", q: "queen", r: "rook", b: "bishop", n: "knight", p: "pawn" };
  const COLOR = { w: "white", b: "black" };
  // <img> for one piece. color: "w" | "b"; kind: "k".."p". Decorative: the square or button carries the label.
  const img = (color, kind, cls) =>
    `<img class="${cls || "pimg"}" src="pieces/${color}${kind.toUpperCase()}.svg" alt="" width="100" height="100" draggable="false">`;
  // A piece from a FEN letter ("N" is a white knight, "n" a black one).
  const pc = (ch) => {
    const color = ch === ch.toUpperCase() ? "w" : "b", kind = ch.toLowerCase();
    return `<span class="pc ${color}" data-piece="${color}${kind}">${img(color, kind)}</span>`;
  };
  const label = (ch) => `${ch === ch.toUpperCase() ? "White" : "Black"} ${NAME[ch.toLowerCase()]}`;
  // The square of the king in check after `san` was played from a position whose fen is `fen` (the position after the move), or null.
  function checkSquare(fen, san) {
    if (!fen || !/[+#]$/.test(san || "")) return null;
    const king = fen.split(" ")[1] === "w" ? "K" : "k", rows = fen.split(" ")[0].split("/");
    for (let r = 0; r < 8; r++) {
      let f = 0;
      for (const ch of rows[r]) {
        if (/\d/.test(ch)) f += +ch;
        else { if (ch === king) return "abcdefgh"[f] + (8 - r); f++; }
      }
    }
    return null;
  }
  // Text tags on squares: words, not only colour.
  const tag = (word, pos) => `<span class="tag ${pos}">${word}</span>`;
  return { KINDS, NAME, COLOR, img, pc, label, checkSquare, tag };
})();
