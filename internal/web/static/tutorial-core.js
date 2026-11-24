// Pure helpers for the Tutorial page: routing, language choice and the
// lesson-board lookups. There is deliberately no chess move generator here.
// Every legal move shown in a lesson is data in tutorial.html (#chess-lessons),
// produced by the Go move generator (github.com/notnil/chess) and checked
// against it by `go test ./internal/web`. This file only reads that data.
//
// It runs in the browser (as globals) and in Node (module.exports) so the logic
// can be unit tested without a browser.
(function (root) {
  "use strict";

  const LANGS = ["en", "ja"];
  const DEFAULT_LANG = "en";
  const LANG_KEY = "gambit.tutorial.lang";
  const PARTS = [1, 2];

  // Which language to use. Order: an explicit ?lang=, then the saved choice, then English.
  function pickLang(queryLang, storedLang) {
    if (LANGS.includes(queryLang)) return queryLang;
    if (LANGS.includes(storedLang)) return storedLang;
    return DEFAULT_LANG;
  }

  // Browser storage can throw (private windows, blocked cookies), so every access is guarded.
  function loadLang(storage) {
    try { const v = storage && storage.getItem(LANG_KEY); return LANGS.includes(v) ? v : null; } catch (e) { return null; }
  }
  function saveLang(storage, lang) {
    try { if (storage && LANGS.includes(lang)) { storage.setItem(LANG_KEY, lang); return true; } } catch (e) { /* ignore */ }
    return false;
  }

  // counts: {1: number of Part 1 steps, 2: number of Part 2 steps}.
  // URL: ?part=1|2&step=N (N counts inside the part) &lang=en|ja.
  // The first version of the tutorial had only the Part 2 steps and linked to ?step=N,
  // so a step with no part still means Part 2.
  function parseRoute(search, counts, storedLang) {
    const q = new URLSearchParams(search || "");
    const num = (v) => (v !== null && /^\d+$/.test(v) ? Number(v) : NaN);
    const rawPart = q.get("part"), rawStep = num(q.get("step"));
    let part = 1;
    if (rawPart !== null) part = PARTS.includes(num(rawPart)) ? num(rawPart) : 1;
    else if (!Number.isNaN(rawStep)) part = 2;
    let step = Number.isNaN(rawStep) ? 1 : rawStep;
    if (step < 1 || step > counts[part]) step = 1;
    return { part, step, lang: pickLang(q.get("lang"), storedLang) };
  }

  // Keeps every other query parameter and rewrites only the tutorial's own.
  function routeSearch(route, currentSearch) {
    const q = new URLSearchParams(currentSearch || "");
    q.set("part", String(route.part));
    q.set("step", String(route.step));
    q.set("lang", route.lang);
    return "?" + q.toString();
  }

  // One path through both parts: flat index 0 .. counts[1]+counts[2]-1.
  function flatIndex(route, counts) { return (route.part === 1 ? 0 : counts[1]) + route.step - 1; }
  function fromFlat(i, counts) {
    const total = counts[1] + counts[2];
    const k = Math.max(0, Math.min(total - 1, i));
    return k < counts[1] ? { part: 1, step: k + 1 } : { part: 2, step: k - counts[1] + 1 };
  }

  // "rnbqkbnr/pppppppp/8/..." -> 64 cells from a8 to h1; "" is an empty square.
  function parseFEN(fen) {
    const cells = [];
    for (const row of fen.split(" ")[0].split("/")) {
      for (const ch of row) {
        if (/[1-8]/.test(ch)) for (let i = 0; i < Number(ch); i++) cells.push("");
        else cells.push(ch);
      }
    }
    return cells;
  }
  const sqIndex = (sq) => (8 - Number(sq[1])) * 8 + "abcdefgh".indexOf(sq[0]);
  const pieceAt = (fen, sq) => parseFEN(fen)[sqIndex(sq)] || "";
  const sideToMove = (fen) => fen.split(" ")[1] === "b" ? "b" : "w";
  const isWhitePiece = (ch) => ch !== "" && ch === ch.toUpperCase();

  // What clicking a square does inside a lesson position. `pos` is one entry of #chess-lessons.
  //   "none"   : the square is empty, or holds a piece with no move data (not the side to move)
  //   "piece"  : a piece of the side to move: its legal moves are returned (possibly none)
  function selection(pos, sq) {
    const list = pos.moves && pos.moves[sq];
    if (!list) return { kind: "none", moves: [] };
    return { kind: "piece", moves: list };
  }
  function moveTo(pos, from, to) {
    const list = (pos.moves && pos.moves[from]) || [];
    return list.find((m) => m.to === to) || null;
  }
  // The position after a move. A promotion needs the piece the player chose: q, r, b or n.
  function resultFEN(move, promo) {
    if (!move) return null;
    if (move.promo) return move.promo[promo] || null;
    return move.after || null;
  }

  // Looks up a string table: t(TEXT, "ja", "ui.next"); falls back to English, then to the key.
  function t(table, lang, key) {
    const get = (l) => key.split(".").reduce((o, k) => (o == null ? o : o[k]), table[l]);
    const v = get(lang);
    if (typeof v === "string") return v;
    const en = get("en");
    return typeof en === "string" ? en : key;
  }

  const api = { LANGS, DEFAULT_LANG, LANG_KEY, pickLang, loadLang, saveLang, parseRoute, routeSearch, flatIndex, fromFlat,
    parseFEN, sqIndex, pieceAt, sideToMove, isWhitePiece, selection, moveTo, resultFEN, t };
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  else root.TutorialCore = api;
})(typeof window !== "undefined" ? window : globalThis);
