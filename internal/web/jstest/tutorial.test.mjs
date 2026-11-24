// Run by `go test ./internal/web` (TestTutorialJS), or directly: node --test internal/web/jstest/tutorial.test.mjs
import test from "node:test";
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { readFileSync } from "node:fs";

const require = createRequire(import.meta.url);
const Core = require("../static/tutorial-core.js");
const { TEXT, PIECES, pieceLabel } = require("../static/tutorial-text.js");

const COUNTS = { 1: 16, 2: 7 };
const html = readFileSync(new URL("../static/tutorial.html", import.meta.url), "utf8");
const lessons = JSON.parse(html.match(/id="chess-lessons">\n([\s\S]*?)\n<\/script>/)[1]).positions;

test("routing: defaults, parts, steps and language", () => {
  assert.deepEqual(Core.parseRoute("", COUNTS, null), { part: 1, step: 1, lang: "en" });
  assert.deepEqual(Core.parseRoute("?part=2&step=4&lang=ja", COUNTS, null), { part: 2, step: 4, lang: "ja" });
  // The first tutorial linked to ?step=N, which was a Part 2 step.
  assert.deepEqual(Core.parseRoute("?step=5", COUNTS, null), { part: 2, step: 5, lang: "en" });
  // Out-of-range or junk values fall back safely.
  assert.deepEqual(Core.parseRoute("?part=1&step=99", COUNTS, null), { part: 1, step: 1, lang: "en" });
  assert.deepEqual(Core.parseRoute("?part=7&step=x&lang=fr", COUNTS, null), { part: 1, step: 1, lang: "en" });
  // The last step of each part is reachable.
  assert.equal(Core.parseRoute("?part=2&step=7", COUNTS, null).step, 7);
});

test("routing: one path through both parts, and the URL round trip", () => {
  assert.deepEqual(Core.fromFlat(0, COUNTS), { part: 1, step: 1 });
  assert.deepEqual(Core.fromFlat(15, COUNTS), { part: 1, step: 16 });
  assert.deepEqual(Core.fromFlat(16, COUNTS), { part: 2, step: 1 });
  assert.deepEqual(Core.fromFlat(99, COUNTS), { part: 2, step: 7 });
  assert.deepEqual(Core.fromFlat(-3, COUNTS), { part: 1, step: 1 });
  for (let i = 0; i < 23; i++) assert.equal(Core.flatIndex(Core.fromFlat(i, COUNTS), COUNTS), i);
  const r = { part: 2, step: 3, lang: "ja" };
  assert.deepEqual(Core.parseRoute(Core.routeSearch(r, "?other=1"), COUNTS, null), r);
  assert.match(Core.routeSearch(r, "?other=1"), /other=1/);
});

test("language: default English, saved choice, URL wins, storage failures are harmless", () => {
  assert.equal(Core.pickLang(null, null), "en");
  assert.equal(Core.pickLang(null, "ja"), "ja");
  assert.equal(Core.pickLang("en", "ja"), "en");
  assert.equal(Core.pickLang("zz", "yy"), "en");
  const mem = new Map();
  const store = { getItem: (k) => mem.get(k) ?? null, setItem: (k, v) => mem.set(k, v) };
  assert.equal(Core.loadLang(store), null);
  assert.equal(Core.saveLang(store, "ja"), true);
  assert.equal(Core.loadLang(store), "ja");
  assert.equal(Core.parseRoute("", COUNTS, Core.loadLang(store)).lang, "ja");
  assert.equal(Core.saveLang(store, "fr"), false);
  const broken = { getItem() { throw new Error("blocked"); }, setItem() { throw new Error("blocked"); } };
  assert.equal(Core.loadLang(broken), null);
  assert.equal(Core.saveLang(broken, "ja"), false);
  assert.equal(Core.loadLang(undefined), null);
});

test("text: both languages cover the same steps and keys, with nothing empty", () => {
  const keys = (o, p = "") => Object.entries(o).flatMap(([k, v]) => (v && typeof v === "object" ? keys(v, p + k + ".") : [p + k]));
  assert.deepEqual(keys(TEXT.ja).sort(), keys(TEXT.en).sort());
  const ctx = (lang) => ({ ui: TEXT[lang].ui, p: (c) => pieceLabel(lang, c), chips: () => "", timeline: () => "", fmtCP: String, a3: {}, a8: { risk: { forecast: 0.5 }, win_chance_drop: 0.5 }, live: null });
  for (const lang of ["en", "ja"]) {
    for (const [id, s] of Object.entries(TEXT[lang].steps)) {
      assert.ok(s.title && s.note, `${lang}.${id} has a title and note`);
      assert.ok(s.body(ctx(lang)).length > 40, `${lang}.${id} has a body`);
    }
  }
  assert.match(TEXT.ja.steps.castle.title, /[぀-ヿ]/);
  // Every step the page routes to has text.
  const ids = [...html.matchAll(/\{ id: "(\w+)"/g)].map((m) => m[1]);
  assert.equal(ids.length, 23);
  for (const id of ids) assert.ok(TEXT.en.steps[id] && TEXT.ja.steps[id], id);
});

test("piece names show the English letter and the Japanese name in both languages", () => {
  for (const [ch, p] of Object.entries(PIECES)) {
    for (const lang of ["en", "ja"]) {
      const s = pieceLabel(lang, ch);
      assert.ok(s.includes(p.ja) && s.includes(p.en), `${lang} ${ch}`);
      if (p.letter) assert.ok(s.includes(`<code>${p.letter}</code>`));
    }
  }
  assert.deepEqual(Object.values(PIECES).map((p) => p.ja), ["キング", "クイーン", "ルーク", "ビショップ", "ナイト", "ポーン"]);
});

test("lesson highlighting reads the bundled data", () => {
  const castle = lessons["castle"];
  assert.equal(Core.pieceAt(castle.fen, "e1"), "K");
  assert.equal(Core.pieceAt(castle.fen, "e4"), "");
  assert.equal(Core.sideToMove(castle.fen), "w");
  const sel = Core.selection(castle, "e1");
  assert.equal(sel.kind, "piece");
  assert.deepEqual(sel.moves.map((m) => m.to).sort(), ["c1", "d1", "d2", "e2", "f1", "f2", "g1"]);
  // Pieces of the side not to move, and empty squares, have no moves to show.
  assert.equal(Core.selection(castle, "e8").kind, "none");
  assert.equal(Core.selection(castle, "e4").kind, "none");
  // Castling shows the rook moving too.
  assert.equal(Core.moveTo(castle, "e1", "g1").kind, "castle");
  assert.match(Core.resultFEN(Core.moveTo(castle, "e1", "g1")), /^r3k2r\/8\/8\/8\/8\/8\/8\/R4RK1 b/);
  assert.equal(Core.moveTo(lessons["castle-blocked"], "e1", "g1"), null);
  // En passant: d6 is offered only right after the double step, and removes the d5 pawn.
  const ep = Core.moveTo(lessons["enpassant"], "e5", "d6");
  assert.equal(ep.kind, "enpassant");
  assert.match(Core.resultFEN(ep), /^4k3\/8\/3P4\/8\/8\/8\/8\/4K3 b/);
  assert.equal(Core.moveTo(lessons["enpassant-late"], "e5", "d6"), null);
  // Promotion needs a choice of piece.
  const pr = Core.moveTo(lessons["promotion"], "e7", "e8");
  assert.equal(pr.kind, "promotion");
  assert.match(Core.resultFEN(pr, "q"), /^4Q2k/);
  assert.match(Core.resultFEN(pr, "n"), /^4N2k/);
  assert.equal(Core.resultFEN(pr, "k"), null);
  assert.equal(Core.moveTo(lessons["promotion-capture"], "e7", "d8").capture, true);
  // Checkmate and stalemate: the king is selectable and has nothing.
  assert.deepEqual(Core.selection(lessons["checkmate"], "g8"), { kind: "piece", moves: [] });
  assert.deepEqual(Core.selection(lessons["stalemate"], "h8"), { kind: "piece", moves: [] });
  // The king lesson never offers the attacked squares.
  const king = Core.selection(lessons["king"], "e3").moves.map((m) => m.to);
  for (const sq of lessons["king"].danger) assert.ok(!king.includes(sq));
});

test("t(): looks up keys and falls back to English, then to the key", () => {
  assert.equal(Core.t(TEXT, "ja", "ui.next"), "次へ ▶");
  assert.equal(Core.t({ en: { a: "x" }, ja: {} }, "ja", "a"), "x");
  assert.equal(Core.t(TEXT, "en", "no.such.key"), "no.such.key");
});
