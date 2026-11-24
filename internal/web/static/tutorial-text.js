// All words shown on the Tutorial page, in English and Japanese.
//
// The Japanese text is machine-assisted. It must be reviewed by a native
// speaker before it is shown to an audience.
//
// Each step is { title, note, body(c) }. `body` returns an HTML string and gets
// a context `c` with helpers from tutorial.html (piece labels, chips, the
// timeline, the sample game's numbers). Code names such as chess.move or
// gen_ai.usage.input_tokens stay in English in both languages: they are what
// the reader will see in Grafana.
(function (root) {
  "use strict";

  // Piece data. letter is the English letter used in written moves; the pawn has none.
  const PIECES = {
    k: { en: "King", ja: "キング", letter: "K", count: 1 },
    q: { en: "Queen", ja: "クイーン", letter: "Q", count: 1 },
    r: { en: "Rook", ja: "ルーク", letter: "R", count: 2 },
    b: { en: "Bishop", ja: "ビショップ", letter: "B", count: 2 },
    n: { en: "Knight", ja: "ナイト", letter: "N", count: 2 },
    p: { en: "Pawn", ja: "ポーン", letter: "", count: 8 },
  };

  // "Rook (ルーク, letter R)" in English, "ルーク (Rook, 記号 R)" in Japanese. Both languages show both names and the letter.
  function pieceLabel(lang, ch) {
    const p = PIECES[ch.toLowerCase()];
    if (lang === "ja") return `<b>${p.ja}</b> (${p.en}${p.letter ? `, 記号 <code>${p.letter}</code>` : ", 記号なし"})`;
    return `<b>${p.en}</b> (<span lang="ja">${p.ja}</span>${p.letter ? `, letter <code>${p.letter}</code>` : ", no letter"})`;
  }
  // Short form for the line under the board: "White rook · ルーク".
  function pieceShort(lang, ch) {
    const p = PIECES[ch.toLowerCase()];
    const white = ch === ch.toUpperCase();
    if (lang === "ja") return `${white ? "白" : "黒"}の${p.ja} (${p.en}${p.letter ? " " + p.letter : ""})`;
    return `${white ? "White" : "Black"} ${p.en.toLowerCase()} (${p.ja}${p.letter ? ", " + p.letter : ""})`;
  }

  const pieceTable = (lang) => {
    const ja = lang === "ja";
    const rows = ["k", "q", "r", "b", "n", "p"].map((ch) => {
      const p = PIECES[ch];
      return `<tr><td><span class="pcg">${{ k: "♚", q: "♛", r: "♜", b: "♝", n: "♞", p: "♟" }[ch]}︎</span></td><td>${p.en}</td><td lang="ja">${p.ja}</td><td>${p.letter ? `<code>${p.letter}</code>` : "–"}</td><td>${p.count}</td></tr>`;
    }).join("");
    return `<table class="ptab"><thead><tr><th></th><th>${ja ? "英語" : "English"}</th><th>${ja ? "日本語" : "日本語"}</th><th>${ja ? "記号" : "Letter"}</th><th>${ja ? "数" : "Each side"}</th></tr></thead><tbody>${rows}</tbody></table>`;
  };

  const en = {
    ui: {
      title: "The Observability Gambit",
      sub: "· tutorial",
      tabRecorded: "Recorded games", tabLive: "Play the agent", tabTutorial: "Tutorial",
      badge: "Sample game · not a result",
      badgeTitle: "The board and Stockfish grades are real; the telemetry and risk numbers are invented to show the idea.",
      keyhint: "step",
      language: "Language",
      part1: "Part 1 · How to play chess", part2: "Part 2 · How to read the coaching",
      part1Short: "Part 1", part2Short: "Part 2",
      parts: "Tutorial parts",
      skipToPart2: "Already know chess? Skip to Part 2",
      backToPart1: "New to chess? Start at Part 1",
      stepOf: "Step {i} of {n}",
      back: "◀ Back", next: "Next ▶", startPart2: "Start Part 2 ▶",
      stepLabel: "Step {i}: {title}",
      reset: "Reset the board",
      legendMove: "can move here", legendCapture: "can capture here", legendSpecial: "special move", legendDanger: "attacked: not allowed", legendCheck: "king in check",
      promoteTo: "Promote the pawn to:",
      sqName: "Square {sq}: {what}",
      empty: "empty",
      picked: "{piece} on {sq}: {n} legal {moves}.",
      pickedNone: "{piece} on {sq}: no legal move.",
      pickedNoMoves: "{piece} on {sq}.",
      moveWord1: "move", moveWordN: "moves",
      played: "You played {from} to {to}.",
      playedCastle: "You played {from} to {to}: castling. The rook moved too.",
      playedEp: "You played {from} to {to}: en passant. The black pawn was removed.",
      playedPromo: "You played {from} to {to} and chose {piece}.",
      playedHint: "Press “Reset the board” to try again.",
      noPieceHere: "Click a highlighted piece.",
      pcWhite: "White", pcBlack: "Black",
    },
    steps: {
      goal: {
        title: "Welcome to chess",
        note: "The start position. Click a piece to see its name.",
        body: (c) => `
      <p>Chess is a game for two players. One player has the <b>white</b> pieces and the other has the <b>black</b> pieces. White moves first. After that, the players take turns. On your turn you move one piece.</p>
      <p>The goal is to <b>checkmate</b> the other player’s king. This means the king is attacked and cannot escape. You will see how in this tutorial.</p>
      <p>Each player starts with 16 pieces. A <b>piece</b> is one of the figures on the board. Click one on the board to see its name.</p>
      ${pieceTable("en")}
      <p class="muted">The letters are used to write moves down, for example <code>Nf3</code>. The Japanese names are the ones used in Japan.</p>
      <p class="skip"><button class="linkbtn" data-goto="part2">${c.ui.skipToPart2} ▸</button></p>`,
      },
      board: {
        title: "The board and its squares",
        note: "Click any square to see its name.",
        body: () => `
      <p>The board has 64 <b>squares</b>, in 8 rows and 8 columns. The squares are light and dark in turn. Each player has a light square at the right-hand corner on their side.</p>
      <p>The columns are called <b>files</b>. They have the letters <b>a</b> to <b>h</b>, from left to right. The rows are called <b>ranks</b>. They have the numbers <b>1</b> to <b>8</b>, from bottom to top. The blue labels on the board show them.</p>
      <p>A square’s name is its file and then its rank. For example, <code>e4</code> is file e, rank 4.</p>
      <p>White starts at the bottom, on ranks 1 and 2. Black starts at the top, on ranks 7 and 8. <b>Forward</b> means toward the other player’s side.</p>
      <p>A <b>diagonal</b> is a line of squares of the same colour that touch at their corners.</p>`,
      },
      rook: {
        title: "The rook",
        note: "Click the rook, then click a highlighted square to move.",
        body: (c) => `
      <p>${c.p("r")}</p>
      <p>The rook moves any number of squares in a straight line: forward, backward, left or right.</p>
      <p>A piece cannot jump over other pieces. The rook stops when another piece is in the way. (The knight is the one piece that can jump.)</p>
      <p><b>Capture</b> means to take an enemy piece. You move your piece onto the square of the enemy piece, and the enemy piece is removed from the board. A piece cannot move onto a square that holds one of its own pieces.</p>
      <p class="muted">Here the rook cannot go past its own pawn on d2. It can capture the black pawns on d7 and b4.</p>`,
      },
      bishop: {
        title: "The bishop",
        note: "The bishop moves on diagonals only.",
        body: (c) => `
      <p>${c.p("b")}</p>
      <p>The bishop moves any number of squares along a diagonal. It cannot jump over pieces.</p>
      <p>A bishop always stays on squares of the same colour. So each player has two bishops: one for the light squares and one for the dark squares.</p>
      <p class="muted">Here the bishop stops before its own pawn on b2. It can capture the black pawn on f6, but it cannot go past it.</p>`,
      },
      queen: {
        title: "The queen",
        note: "The queen moves like a rook and a bishop together.",
        body: (c) => `
      <p>${c.p("q")}</p>
      <p>The queen moves any number of squares in a straight line or along a diagonal, in any direction. It is like a rook and a bishop in one piece. It cannot jump over pieces.</p>
      <p>The queen is the strongest piece. Each player has one queen.</p>`,
      },
      knight: {
        title: "The knight",
        note: "The knight jumps in an L-shape.",
        body: (c) => `
      <p>${c.p("n")}</p>
      <p>The knight moves in an <b>L-shape</b>: two squares in a straight line, then one square to the side. Or one square in a straight line, then two squares to the side.</p>
      <p>The knight is the only piece that can <b>jump over</b> other pieces, its own or the enemy’s. It captures only the piece on the square where it lands. It cannot land on its own piece.</p>
      <p class="muted">Here the pawns next to the knight do not stop it. It cannot land on f5, because its own pawn is there. It can capture the black pawn on c6.</p>`,
      },
      king: {
        title: "The king",
        note: "The ✕ squares are attacked by black pawns, so the king cannot go there.",
        body: (c) => `
      <p>${c.p("k")}</p>
      <p>The king moves one square in any direction. The king is the most important piece: if it is checkmated, you lose the game.</p>
      <p>A piece <b>attacks</b> a square if it could capture an enemy piece on that square on its next move. The king can never move onto an attacked square. A black pawn captures diagonally forward, which for Black is down the board. So these black pawns attack <code>e2</code> and <code>e4</code>. Those squares are marked ✕.</p>
      <p>The king can capture an enemy piece next to it, but only if no other enemy piece protects it. The two kings can never stand on squares that touch.</p>`,
      },
      pawn: {
        title: "The pawn",
        note: "Try the pawns on e2, c4 and g2.",
        body: (c) => `
      <p>${c.p("p")}</p>
      <p>A pawn moves <b>forward</b> one square. It never moves backward or sideways. It cannot move if any piece, even an enemy piece, is on the square in front of it.</p>
      <p>A pawn on its starting square (rank 2 for White) may move forward <b>two</b> squares, but only if both squares are empty.</p>
      <p>A pawn captures in a different way from how it moves: it captures one square <b>diagonally forward</b>. It cannot capture straight ahead.</p>
      <p class="muted">Click each pawn. e2 can move two squares. c4 can move one square and can capture on b5 and d5. g2 is blocked by the black pawn on g3, but it can capture the knight on h3.</p>
      <p>Pawns also have two special moves, <b>en passant</b> and <b>promotion</b>. You will see them soon.</p>`,
      },
      check: {
        title: "Check",
        note: "White is in check from the rook on e8. Click each white piece.",
        body: () => `
      <p><b>Check</b> means a king is attacked. A player in check must make a move that ends the check right away. There are three ways:</p>
      <ul>
        <li><b>Move the king</b> to a square that is not attacked.</li>
        <li><b>Block</b>: put another piece between the attacker and the king. This is not possible against a knight, because a knight jumps.</li>
        <li><b>Capture</b> the attacking piece.</li>
      </ul>
      <p>You are never allowed to make a move that leaves your own king in check. In this position, click the king, the bishop and the rook to see their only legal moves.</p>
      <p class="muted">In written moves, a <code>+</code> after a move means it gives check.</p>`,
      },
      checkmate: {
        title: "Checkmate",
        note: "Black is checkmated. Click the black king.",
        body: () => `
      <p><b>Checkmate</b> means the king is in check and there is no legal move that ends the check. The game is over, and the player who gave checkmate wins.</p>
      <p>Here the white rook on a8 attacks the black king along rank 8. The king cannot move to f8 or h8, because the rook attacks those squares. The king cannot go to f7, g7 or h7, because its own pawns are there. No piece can block or capture the rook.</p>
      <p class="muted">In written moves, a <code>#</code> after a move means checkmate. This position comes from <code>Ra8#</code>.</p>`,
      },
      draw: {
        title: "Stalemate and other draws",
        note: "Black to move. Click the black king.",
        body: () => `
      <p>A <b>draw</b> means nobody wins. These are the ways a game can be drawn:</p>
      <ul>
        <li><b>Stalemate</b>: the player to move is <i>not</i> in check but has no legal move. In the first position, Black is to move. The black king is not in check, but every square next to it is attacked, and Black has no other piece.</li>
        <li><b>Not enough pieces</b>: neither player can checkmate. For example, king against king, or king and one bishop or one knight against king (second position).</li>
        <li><b>Repetition</b>: the same position appears three times. A player can ask for a draw.</li>
        <li><b>Fifty-move rule</b>: each player makes 50 moves with no capture and no pawn move. A player can ask for a draw.</li>
        <li><b>Agreement</b>: both players agree to a draw.</li>
      </ul>
      <p class="muted">Compare with checkmate: in checkmate the king <i>is</i> in check. In stalemate it is not.</p>`,
        examples: { stalemate: "Stalemate", kings: "King against king" },
      },
      castle: {
        title: "Castling",
        note: "Click the white king, then click g1 or c1.",
        body: () => `
      <p><b>Castling</b> is a special move that moves two pieces at once: the king moves <b>two squares</b> toward a rook, and that rook jumps to the square on the other side of the king. It helps keep the king safe, and it brings the rook into the game.</p>
      <p>Castling is allowed only when:</p>
      <ul>
        <li>the king and that rook have <b>never moved</b>;</li>
        <li>all squares between them are <b>empty</b>;</li>
        <li>the king is <b>not in check</b>;</li>
        <li>the king does not pass over, or land on, an <b>attacked</b> square.</li>
      </ul>
      <p>Castling toward the h-file rook is <b>kingside</b> and is written <code>O-O</code>. Castling toward the a-file rook is <b>queenside</b> and is written <code>O-O-O</code>.</p>
      <p class="muted">In the second example, the black rook attacks f1. The king would pass over f1, so kingside castling is not allowed. Queenside castling still is.</p>`,
        examples: { castle: "Both allowed", "castle-blocked": "Kingside not allowed" },
      },
      enpassant: {
        title: "En passant",
        note: "Click the white pawn on e5, then click d6.",
        body: () => `
      <p><b>En passant</b> is a French phrase for “in passing”. It is a special pawn capture.</p>
      <p>Suppose an enemy pawn moves forward <b>two squares</b> from its starting square and lands <b>next to your pawn</b>, on the same rank. On your very next move, you may capture it as if it had moved only one square. Your pawn moves diagonally onto the square the enemy pawn skipped, and the enemy pawn is removed.</p>
      <p>In the first example, Black has just played <code>d7-d5</code>, next to the white pawn on e5. White can capture on d6.</p>
      <p><b>You must do it at once.</b> If you play another move, the chance is gone. In the second example, the same position arises one move later, and the capture is not possible.</p>`,
        examples: { enpassant: "Black just played d7-d5", "enpassant-late": "One move later" },
      },
      promotion: {
        title: "Promotion",
        note: "Click the pawn, then click e8 and choose a piece.",
        body: () => `
      <p>When a pawn reaches the far end of the board (rank 8 for White, rank 1 for Black), it must become another piece. This is <b>promotion</b>. You choose a queen, a rook, a bishop or a knight of your own colour. It cannot stay a pawn, and it cannot become a king.</p>
      <p>Most players choose the queen, because it is the strongest piece. A pawn can also promote by capturing, as in the second example.</p>
      <p class="muted">In written moves, <code>e8=Q</code> means a pawn reached e8 and became a queen.</p>`,
        examples: { promotion: "Push to e8", "promotion-capture": "Capture and promote" },
      },
      ideas: {
        title: "Four ideas for beginners",
        note: "White has taken the centre, developed pieces and castled.",
        body: () => `
      <ol>
        <li><b>Take the centre.</b> The four middle squares, d4, e4, d5 and e5, are the most important. Pieces there can reach many squares. Often a pawn to e4 or d4 is a good first move.</li>
        <li><b>Develop your pieces.</b> To <b>develop</b> is to move a piece from its starting square to a place where it does more. Bring out your knights and bishops early, and do not move the same piece again and again in the first moves.</li>
        <li><b>Keep your king safe.</b> Castle early, so the king is behind its pawns.</li>
        <li><b>Do not give pieces away.</b> Before each move, ask: can the enemy capture the piece I move, or another of my pieces? Use this guide to the value of pieces: pawn 1, knight 3, bishop 3, rook 5, queen 9. Do not trade a high-value piece for a low-value one.</li>
      </ol>
      <p class="muted">The numbers are only a guide, not a rule. The king has no value because the game ends if it is lost.</p>`,
      },
      bridge: {
        title: "You can read a chess game now",
        note: "The sample game, which you will use in Part 2.",
        body: (c) => `
      <p>You now know how every piece moves and how a game ends. That is enough to follow the rest of this tutorial.</p>
      <p><b>Part 2</b> shows what the app records while an AI agent plays chess: the move written down, the trace of each decision, how a blunder is graded, and how the telemetry can warn before a blunder.</p>
      <div class="ctas"><button class="primary" data-goto="part2">${c.ui.startPart2}</button></div>
      <p class="muted">You can come back to Part 1 at any time with the tabs above.</p>`,
      },

      // ---------------------------------------------------------------- Part 2
      watching: {
        title: "What you are watching",
        note: "The starting position. Everything here is a built-in sample game, so this tutorial works offline.",
        body: () => `
      <p>An AI agent, a language model, is playing chess against <b>Stockfish</b>, a very strong chess program.</p>
      <p>Every move the agent makes is recorded as an <b>OpenTelemetry trace</b>: how long it took, how many tokens it used, whether it tried an illegal move.</p>
      <p>After each move, a second Stockfish <b>grades</b> it. That gives observability something it rarely has: an objective answer to “was that a good decision?”</p>
      <p>The question behind the talk: <b>can the traces show a blunder coming?</b></p>
      <p class="muted">Two ways to see it: <b>Recorded games</b> replays finished games. <b>Play the agent</b> (human mode) lets you take the other side, while the agent is traced and graded in the same way.</p>`,
      },
      notation: {
        title: "Reading a move",
        note: "After 1. e4 e5 2. Nf3. Yellow marks the last move. The ringed square is f3.",
        body: () => `
      <p>The app writes moves in short notation. <code>Nf3</code> means a knight moved to f3. The ringed square is <b>f3</b>.</p>
      <p>The first part is the piece: <code>N</code> knight, <code>B</code> bishop, <code>R</code> rook, <code>Q</code> queen, <code>K</code> king. A pawn has no letter. The rest is the square it moves to.</p>
      <p>Extra signs: <code>x</code> is a capture (<code>Nxg5</code>), <code>O-O</code> and <code>O-O-O</code> are castling, <code>+</code> is check, <code>#</code> is checkmate, and <code>=Q</code> is promotion to a queen.</p>
      <p class="muted">When the agent plays Black, the board is flipped so its side is at the bottom.</p>`,
      },
      trace: {
        title: "One move, one trace",
        note: "The agent is about to be asked for its 8th move.",
        body: () => `
      <p>Each time the agent moves, one trace is recorded. This is its 8th move in the sample.</p>
      <ul class="tree" aria-label="Trace for move 8">
        <li><span class="n">chess.move</span> <span class="t">root · 7.9 s</span><span class="w">The whole move: ply, FEN, legal-move count, and later the grade.</span>
          <ul>
            <li><span class="n">invoke_agent chess-agent</span> <span class="t">6.8 s</span><span class="w">The decision. Its duration is the latency.</span>
              <ul>
                <li><span class="n">chat sample-model</span> <span class="t">2.1 s</span><span class="w">One model call: tokens in and out, finish reason.</span></li>
                <li><span class="n">execute_tool legal_moves</span> <span class="t">3 ms</span><span class="w">The model asked for a tool.</span></li>
                <li><span class="n">chat sample-model</span> <span class="t">2.4 s</span></li>
                <li class="err"><span class="n">chess.validate_move</span> <span class="t">illegal_move</span><span class="w">The model proposed <code>Nxg6</code>, which is not legal here.</span></li>
                <li><span class="n">chat sample-model</span> <span class="t">2.3 s</span><span class="w">The retry.</span></li>
                <li><span class="n">chess.validate_move</span> <span class="t">legal</span><span class="w">Candidate <code>Nxg5</code> is legal, so it is played.</span></li>
              </ul>
            </li>
            <li><span class="n">chess.evaluate</span> <span class="t">1.1 s</span><span class="w">Stockfish’s grade. A sibling of the decision, so its time is not in the latency.</span></li>
          </ul>
        </li>
      </ul>
      <p class="muted">The <code>chat</code>, <code>invoke_agent</code> and <code>execute_tool</code> spans use the OpenTelemetry GenAI conventions (<code>gen_ai.*</code>). Names starting <code>chess.</code> and <code>gambit.</code> are this project’s own. Timings in this sample are invented.</p>`,
      },
      numbers: {
        title: "The numbers on a move",
        note: "Same position as the last step. Orange chips mark retries and extra calls.",
        body: (c) => `
      <p>The replay turns each trace into a few numbers. Compare an early move with the 8th:</p>
      <div class="rowlabel">Move 3</div>${c.chips(c.a3)}
      <div class="rowlabel">Move 8</div>${c.chips(c.a8)}
      <dl class="keys">
        <dt>latency</dt><dd>How long the decision took, the <code>invoke_agent</code> duration.</dd>
        <dt>chat / tool calls</dt><dd>Model calls and tool calls in one decision: <code>gambit.chat_calls</code>, <code>gambit.tool_calls</code>.</dd>
        <dt>in / out tok</dt><dd>Tokens summed over every call: <code>gen_ai.usage.input_tokens</code>, <code>gen_ai.usage.output_tokens</code>.</dd>
        <dt>illegal tries</dt><dd>Moves proposed that were not legal: <code>chess.illegal_attempts</code>. After three failed retries the agent plays a random legal move and sets <code>chess.fallback</code>.</dd>
      </dl>`,
        chipNames: { latency: "latency", chat: "chat calls", inTok: "in tok", outTok: "out tok", tool: "tool calls", illegal: "illegal tries" },
      },
      grading: {
        title: "How a blunder is graded",
        note: "Red is the move that was played. The green ring is where Stockfish would have gone: Bg3.",
        body: (c) => `
      <p>After the move, Stockfish scores the position before and after, at a fixed depth. The grade is how much the <b>chance of winning</b> dropped.</p>
      <div class="grades">
        <div><span class="cls" style="background:var(--best)">best</span><span class="muted">matched Stockfish’s top move</span></div>
        <div><span class="cls" style="background:var(--good)">good</span><span class="muted">under 5 points lost</span></div>
        <div><span class="cls" style="background:var(--inaccuracy)">inaccuracy</span><span class="muted">5 points or more</span></div>
        <div><span class="cls" style="background:var(--mistake)">mistake</span><span class="muted">10 points or more</span></div>
        <div><span class="cls" style="background:var(--blunder)">blunder</span><span class="muted">15 points or more</span></div>
      </div>
      <p><b>Move 8, Nxg5:</b> the evaluation went from ${c.fmtCP(c.a8.eval_before_cp)} to ${c.fmtCP(c.a8.eval_after_cp)} and the chance of winning dropped about ${(c.a8.win_chance_drop * 50).toFixed(0)} points. That is a <span class="cls" style="background:var(--blunder)">blunder</span>. The knight takes a pawn and is then captured by the h-pawn (<code>hxg5</code>).</p>
      <p class="muted">The grade is stored on the trace as <code>chess.move.class</code>, with the evaluation next to it, and it is also sent as a <code>gen_ai.evaluation.result</code> event.</p>`,
      },
      predict: {
        title: "Predicting a blunder from telemetry",
        note: "The agent is about to be asked for move 8.",
        body: (c) => `
      <p>The grade only exists <i>after</i> the move. The talk asks whether the traces give a warning earlier. A small model scores the risk of a blunder from trace features such as latency, tokens, retries and tool calls:</p>
      <ul>
        <li><b style="color:var(--risk)">Forecast</b> uses the position and the traces of the <i>previous</i> moves only. It can warn before the agent is even asked.</li>
        <li><b style="color:var(--risk2)">At-move</b> also sees this move’s own trace, so it can veto a move before it is played.</li>
      </ul>
      ${c.timeline()}
      <div class="legend"><span style="color:var(--risk)">Forecast</span><span style="color:var(--risk2)">At-move</span><span style="color:var(--muted)">Alert threshold</span></div>
      <div class="callout alert"><b>Incoming: agent’s move 8</b><div style="font-size:20px;font-weight:700;margin-top:4px;color:var(--blunder)">${(c.a8.risk.forecast * 100).toFixed(0)}% risk · warning before it is requested</div></div>
      <p class="muted" style="margin-top:10px">These risk numbers are invented for the sample. They show what the replay draws; they are not a result.</p>`,
        timelineLabel: "Sample risk by move: the forecast crosses the alert threshold at move 8, the blunder",
        timelineMove: "{n}. {san} · {cls}",
      },
      try: {
        title: "Try it yourself",
        note: "The sample game after 8… hxg5.",
        body: (c) => `
      <p>Now try the real thing:</p>
      <div class="ctas">
        <a class="btn primary" href="index.html">${c.ui.tabRecorded} ▸</a>
        <a class="btn ${c.live ? "primary" : "off"}" href="live.html" ${c.live ? "" : 'aria-disabled="true" tabindex="-1"'}>${c.ui.tabLive} ▸</a>
      </div>
      <p class="muted">${c.live ? "Human mode is on here: you play one side and the agent plays the other." : "Human mode is off on this server. Start it with <code>gambit serve --live</code> to play the agent."}</p>
      <ul>
        <li>In a replay, press <kbd>b</kbd> to jump to the next blunder, or click a dot on the timeline.</li>
        <li>Step with <kbd>←</kbd> <kbd>→</kbd> and watch the risk bars before each agent move.</li>
        <li><b>Open trace ↗</b> jumps from a move into its full trace.</li>
        <li>In <b>Play the agent</b>, a coach comments on each of your moves. Use <b>Ask the coach why</b> on any move to hear the reason.</li>
      </ul>
      <p class="muted">Need a rule again? Go back to <button class="linkbtn" data-goto="part1">Part 1</button>.</p>`,
      },
    },
  };

  const ja = {
    ui: {
      title: "The Observability Gambit",
      sub: "· チュートリアル",
      tabRecorded: "記録された対局", tabLive: "エージェントと対戦", tabTutorial: "チュートリアル",
      badge: "サンプル対局・実際の結果ではありません",
      badgeTitle: "盤面とStockfishの評価は本物です。テレメトリとリスクの数値は、仕組みを示すために作った例です。",
      keyhint: "ステップ",
      language: "言語",
      part1: "パート1 · チェスの遊び方", part2: "パート2 · コーチングの読み方",
      part1Short: "パート1", part2Short: "パート2",
      parts: "チュートリアルのパート",
      skipToPart2: "チェスを知っている人は、パート2へ進む",
      backToPart1: "チェスが初めての人は、パート1から始める",
      stepOf: "ステップ {i} / {n}",
      back: "◀ 戻る", next: "次へ ▶", startPart2: "パート2を始める ▶",
      stepLabel: "ステップ {i}: {title}",
      reset: "盤面をもとに戻す",
      legendMove: "ここへ動ける", legendCapture: "ここで駒を取れる", legendSpecial: "特別な動き", legendDanger: "攻撃されている: 動けない", legendCheck: "チェックされているキング",
      promoteTo: "ポーンを何に変えますか:",
      sqName: "マス {sq}: {what}",
      empty: "空",
      picked: "{piece}（{sq}）: 動ける場所は {n} か所です。",
      pickedNone: "{piece}（{sq}）: 動ける場所はありません。",
      pickedNoMoves: "{piece}（{sq}）。",
      moveWord1: "", moveWordN: "",
      played: "{from} から {to} へ動かしました。",
      playedCastle: "{from} から {to} へ動かしました: キャスリングです。ルークも動きました。",
      playedEp: "{from} から {to} へ動かしました: アンパッサンです。黒のポーンが取り除かれました。",
      playedPromo: "{from} から {to} へ動かし、{piece}を選びました。",
      playedHint: "「盤面をもとに戻す」を押すと、もう一度試せます。",
      noPieceHere: "光っている駒をクリックしてください。",
      pcWhite: "白", pcBlack: "黒",
    },
    steps: {
      goal: {
        title: "チェスへようこそ",
        note: "最初の配置です。駒をクリックすると名前が出ます。",
        body: (c) => `
      <p>チェスは2人で遊ぶゲームです。一方が<b>白</b>の駒、もう一方が<b>黒</b>の駒を使います。最初に動かすのは白です。そのあとは、交代で動かします。自分の番には、駒を1つ動かします。</p>
      <p>目的は、相手のキングを<b>チェックメイト</b>することです。これは、キングが攻撃されていて、逃げられない状態のことです。やり方はこのチュートリアルで説明します。</p>
      <p>それぞれのプレイヤーは、16個の<b>駒</b>（盤の上の1つ1つの形）で始めます。盤の上の駒をクリックすると、名前が出ます。</p>
      ${pieceTable("ja")}
      <p class="muted">記号（アルファベット）は、動きを書くときに使います。例: <code>Nf3</code>。</p>
      <p class="skip"><button class="linkbtn" data-goto="part2">${c.ui.skipToPart2} ▸</button></p>`,
      },
      board: {
        title: "盤とマス",
        note: "マスをクリックすると、そのマスの名前が出ます。",
        body: () => `
      <p>盤には、縦8列、横8行の合計64個の<b>マス</b>があります。マスは、明るい色と暗い色が交互に並んでいます。自分側の右手前の角は、明るいマスです。</p>
      <p>縦の列を<b>ファイル</b>といいます。左から右へ、<b>a</b>から<b>h</b>までの文字がついています。横の行を<b>ランク</b>といいます。下から上へ、<b>1</b>から<b>8</b>までの数字がついています。盤の上の青いラベルがそれです。</p>
      <p>マスの名前は、ファイルの文字のあとにランクの数字を書きます。例: <code>e4</code> は、ファイルe、ランク4のマスです。</p>
      <p>白は下側の、ランク1と2から始めます。黒は上側の、ランク7と8から始めます。<b>前</b>とは、相手側へ向かう方向です。</p>
      <p><b>斜め</b>とは、同じ色のマスが角でつながって並んでいる線のことです。</p>`,
      },
      rook: {
        title: "ルーク",
        note: "ルークをクリックして、光っているマスをクリックすると動きます。",
        body: (c) => `
      <p>${c.p("r")}</p>
      <p>ルークは、まっすぐな線の上を、何マスでも動けます。前、後ろ、左、右です。</p>
      <p>駒は、ほかの駒を飛び越えられません。間に駒があると、ルークはそこで止まります。（飛び越えられるのはナイトだけです。）</p>
      <p><b>取る</b>とは、相手の駒を取り除くことです。自分の駒を、相手の駒があるマスへ動かします。すると、相手の駒は盤から取り除かれます。自分の駒があるマスへは動けません。</p>
      <p class="muted">この図では、ルークは自分のポーン（d2）より先へ進めません。黒のポーン（d7とb4）は取れます。</p>`,
      },
      bishop: {
        title: "ビショップ",
        note: "ビショップは斜めにだけ動きます。",
        body: (c) => `
      <p>${c.p("b")}</p>
      <p>ビショップは、斜めの線の上を、何マスでも動けます。駒を飛び越えることはできません。</p>
      <p>ビショップは、ずっと同じ色のマスの上にいます。だから、それぞれのプレイヤーは2つのビショップを持ちます。1つは明るいマス用、もう1つは暗いマス用です。</p>
      <p class="muted">この図では、ビショップは自分のポーン（b2）の手前で止まります。黒のポーン（f6）は取れますが、その先へは進めません。</p>`,
      },
      queen: {
        title: "クイーン",
        note: "クイーンは、ルークとビショップを合わせた動きをします。",
        body: (c) => `
      <p>${c.p("q")}</p>
      <p>クイーンは、まっすぐな線または斜めの線の上を、どの方向にも何マスでも動けます。ルークとビショップを1つにした動きです。駒を飛び越えることはできません。</p>
      <p>クイーンは最も強い駒です。それぞれのプレイヤーは、クイーンを1つ持ちます。</p>`,
      },
      knight: {
        title: "ナイト",
        note: "ナイトは、L字の形に跳びます。",
        body: (c) => `
      <p>${c.p("n")}</p>
      <p>ナイトは<b>L字</b>に動きます。まっすぐ2マス進んでから、横に1マス動きます。または、まっすぐ1マス進んでから、横に2マス動きます。</p>
      <p>ナイトは、ほかの駒を<b>飛び越えられる</b>唯一の駒です。自分の駒も相手の駒も飛び越えられます。取れるのは、着地したマスにある駒だけです。自分の駒があるマスには着地できません。</p>
      <p class="muted">この図では、ナイトの隣にあるポーンは、ナイトの邪魔になりません。f5には自分のポーンがあるので、着地できません。黒のポーン（c6）は取れます。</p>`,
      },
      king: {
        title: "キング",
        note: "✕のマスは黒のポーンに攻撃されています。キングは動けません。",
        body: (c) => `
      <p>${c.p("k")}</p>
      <p>キングは、どの方向にも1マス動きます。キングは最も大切な駒です。チェックメイトされると、そのゲームに負けます。</p>
      <p>駒が次の1手で、そのマスにいる相手の駒を取れるとき、その駒はそのマスを<b>攻撃している</b>といいます。キングは、攻撃されているマスへは動けません。黒のポーンは、斜め前を取ります。黒にとっての前は、盤の下の方向です。だからこの黒のポーンは、<code>e2</code> と <code>e4</code> を攻撃しています。そのマスに✕がついています。</p>
      <p>キングは、隣にある相手の駒を取れます。ただし、その駒をほかの相手の駒が守っていないときだけです。2つのキングは、隣り合うマスには立てません。</p>`,
      },
      pawn: {
        title: "ポーン",
        note: "e2、c4、g2のポーンを試してください。",
        body: (c) => `
      <p>${c.p("p")}</p>
      <p>ポーンは、<b>前</b>へ1マス動きます。後ろや横へは動けません。前のマスに駒があると、相手の駒であっても動けません。</p>
      <p>最初のマス（白ならランク2）にいるポーンは、前のマスが2つとも空いているときだけ、<b>2マス</b>進めます。</p>
      <p>ポーンは、動き方と取り方が違います。取るときは、<b>斜め前</b>に1マス動きます。まっすぐ前の駒は取れません。</p>
      <p class="muted">それぞれのポーンをクリックしてみましょう。e2は2マス進めます。c4は1マス進めて、b5とd5で取れます。g2は、g3の黒のポーンに止められますが、h3のナイトは取れます。</p>
      <p>ポーンには、<b>アンパッサン</b>と<b>プロモーション</b>という特別な動きもあります。このあとで説明します。</p>`,
      },
      check: {
        title: "チェック",
        note: "白は、e8のルークからチェックされています。白の駒をクリックしてみましょう。",
        body: () => `
      <p><b>チェック</b>とは、キングが攻撃されている状態です。チェックされたプレイヤーは、すぐにチェックを終わらせる手を指さなければなりません。方法は3つあります。</p>
      <ul>
        <li>キングを、攻撃されていないマスへ<b>動かす</b>。</li>
        <li>攻撃している駒とキングの間に、ほかの駒を置いて<b>防ぐ</b>。ナイトは飛び越えるので、ナイトからのチェックは防げません。</li>
        <li>攻撃している駒を<b>取る</b>。</li>
      </ul>
      <p>自分のキングがチェックされたままになる手は、指せません。この図で、キング、ビショップ、ルークをクリックして、指せる手を見てみましょう。</p>
      <p class="muted">書いた手の最後に <code>+</code> があれば、その手がチェックになることを表します。</p>`,
      },
      checkmate: {
        title: "チェックメイト",
        note: "黒はチェックメイトされています。黒のキングをクリックしてみましょう。",
        body: () => `
      <p><b>チェックメイト</b>とは、キングがチェックされていて、チェックを終わらせる手が1つもない状態です。ゲームは終わり、チェックメイトした人が勝ちです。</p>
      <p>この図では、a8の白のルークが、ランク8に沿って黒のキングを攻撃しています。キングは、ルークに攻撃されているf8とh8へは動けません。f7、g7、h7には自分のポーンがあるので、動けません。ルークを防ぐ駒も、取る駒もありません。</p>
      <p class="muted">書いた手の最後に <code>#</code> があれば、チェックメイトを表します。この図は <code>Ra8#</code> の局面です。</p>`,
      },
      draw: {
        title: "ステイルメイトと、そのほかの引き分け",
        note: "黒の番です。黒のキングをクリックしてみましょう。",
        body: () => `
      <p><b>引き分け</b>とは、どちらも勝たないことです。引き分けになるのは、次の場合です。</p>
      <ul>
        <li><b>ステイルメイト</b>: 番のプレイヤーが、チェックされて<i>いない</i>のに、指せる手が1つもない場合。1つ目の図は黒の番です。黒のキングはチェックされていませんが、隣のマスがすべて攻撃されています。黒にはほかの駒がありません。</li>
        <li><b>駒が足りない</b>: どちらもチェックメイトできない場合。例: キング対キング、またはキングと、ビショップ1つかナイト1つ、対キング（2つ目の図）。</li>
        <li><b>同じ局面の繰り返し</b>: 同じ局面が3回出た場合。プレイヤーは引き分けを求められます。</li>
        <li><b>50手ルール</b>: 両方のプレイヤーが、駒を取らず、ポーンも動かさずに、それぞれ50手指した場合。プレイヤーは引き分けを求められます。</li>
        <li><b>合意</b>: 2人が引き分けに同意した場合。</li>
      </ul>
      <p class="muted">チェックメイトと比べましょう。チェックメイトでは、キングはチェックされています。ステイルメイトでは、されていません。</p>`,
        examples: { stalemate: "ステイルメイト", kings: "キング対キング" },
      },
      castle: {
        title: "キャスリング",
        note: "白のキングをクリックして、g1かc1をクリックしてください。",
        body: () => `
      <p><b>キャスリング</b>は、2つの駒を一度に動かす特別な動きです。キングが、ルークのほうへ<b>2マス</b>動きます。そして、そのルークが、キングの反対側の隣のマスへ跳びます。キングを安全にし、ルークを使いやすい場所へ出せます。</p>
      <p>キャスリングができるのは、次の条件がすべて満たされているときだけです。</p>
      <ul>
        <li>キングと、そのルークが、<b>一度も動いていない</b>。</li>
        <li>その間のマスが、すべて<b>空いている</b>。</li>
        <li>キングが<b>チェックされていない</b>。</li>
        <li>キングが通るマスと、着くマスが、<b>攻撃されていない</b>。</li>
      </ul>
      <p>hファイルのルークのほうへのキャスリングは<b>キングサイド</b>といい、<code>O-O</code> と書きます。aファイルのルークのほうへのキャスリングは<b>クイーンサイド</b>といい、<code>O-O-O</code> と書きます。</p>
      <p class="muted">2つ目の例では、黒のルークがf1を攻撃しています。キングはf1を通るので、キングサイドのキャスリングはできません。クイーンサイドはできます。</p>`,
        examples: { castle: "どちらもできる", "castle-blocked": "キングサイドはできない" },
      },
      enpassant: {
        title: "アンパッサン",
        note: "e5の白のポーンをクリックして、d6をクリックしてください。",
        body: () => `
      <p><b>アンパッサン</b>は、フランス語で「通りすがりに」という意味です。ポーンだけの特別な取り方です。</p>
      <p>相手のポーンが、最初のマスから<b>2マス</b>進んで、自分のポーンの<b>隣</b>（同じランク）に止まったとします。そのとき、自分の次の1手だけは、そのポーンが1マスしか進まなかったものとして取れます。自分のポーンは、相手のポーンが通り過ぎたマスへ斜めに動き、相手のポーンは取り除かれます。</p>
      <p>1つ目の例では、黒がちょうど <code>d7-d5</code> と指し、白のポーン（e5）の隣に来ました。白はd6へ動いて取れます。</p>
      <p><b>すぐに取らなければなりません。</b>ほかの手を指すと、チャンスはなくなります。2つ目の例は、1手あとの同じ局面です。取れません。</p>`,
        examples: { enpassant: "黒がd7-d5と指した直後", "enpassant-late": "1手あと" },
      },
      promotion: {
        title: "プロモーション",
        note: "ポーンをクリックして、e8をクリックし、駒を選んでください。",
        body: () => `
      <p>ポーンが盤の反対側の端（白ならランク8、黒ならランク1）に着くと、ほかの駒に変わらなければなりません。これが<b>プロモーション</b>です。自分の色のクイーン、ルーク、ビショップ、ナイトのどれかを選びます。ポーンのままではいられません。キングにもなれません。</p>
      <p>多くの人は、最も強いクイーンを選びます。2つ目の例のように、ポーンは駒を取りながらプロモーションすることもできます。</p>
      <p class="muted">書いた手の <code>e8=Q</code> は、ポーンがe8に着いて、クイーンになったことを表します。</p>`,
        examples: { promotion: "e8へ進む", "promotion-capture": "取ってプロモーション" },
      },
      ideas: {
        title: "初心者のための4つの考え方",
        note: "白は、中央を取り、駒を展開し、キャスリングしました。",
        body: () => `
      <ol>
        <li><b>中央を取る。</b>真ん中の4マス（d4、e4、d5、e5）が最も大切です。そこにある駒は、多くのマスへ動けます。最初の手に、eポーンやdポーンを進めるのは、よい手であることが多いです。</li>
        <li><b>駒を展開する。</b><b>展開</b>とは、駒を最初のマスから、より働く場所へ動かすことです。ナイトとビショップを早く出しましょう。最初のうちは、同じ駒を何度も動かさないようにします。</li>
        <li><b>キングを安全にする。</b>早めにキャスリングして、キングをポーンの後ろに置きます。</li>
        <li><b>駒をただで取られない。</b>手を指す前に、確かめましょう。動かす駒や、ほかの自分の駒を、相手が取れないでしょうか。駒の価値の目安は、ポーン1、ナイト3、ビショップ3、ルーク5、クイーン9です。価値の高い駒を、価値の低い駒と交換するのは避けましょう。</li>
      </ol>
      <p class="muted">この数字は目安であり、ルールではありません。キングを取られるとゲームが終わるので、キングには価値の数字がありません。</p>`,
      },
      bridge: {
        title: "これでチェスの対局が読めます",
        note: "パート2で使うサンプル対局です。",
        body: (c) => `
      <p>すべての駒の動き方と、ゲームの終わり方がわかりました。このチュートリアルの続きを読むには、これで十分です。</p>
      <p><b>パート2</b>では、AIエージェントがチェスを指しているときに、このアプリが記録するものを説明します。書き留められた手、1つ1つの判断のトレース、ブランダー（大きな悪手）の評価の仕方、そして、テレメトリでブランダーを事前に知らせる方法です。</p>
      <div class="ctas"><button class="primary" data-goto="part2">${c.ui.startPart2}</button></div>
      <p class="muted">上のタブから、いつでもパート1に戻れます。</p>`,
      },

      // ---------------------------------------------------------------- Part 2
      watching: {
        title: "いま見ているもの",
        note: "最初の配置です。ここにあるものはすべて組み込みのサンプル対局なので、このチュートリアルはオフラインでも動きます。",
        body: () => `
      <p>AIエージェント（言語モデル）が、とても強いチェスプログラムの<b>Stockfish</b>と対戦しています。</p>
      <p>エージェントの1手1手は、<b>OpenTelemetryのトレース</b>として記録されます。かかった時間、使ったトークンの数、反則の手を試したかどうか、などです。</p>
      <p>1手ごとに、もう1つのStockfishがその手を<b>評価</b>します。これにより、オブザーバビリティにとって珍しいものが手に入ります。「それはよい判断だったか」という、客観的な答えです。</p>
      <p>この講演の問いは、<b>トレースから、ブランダー（大きな悪手）の前兆が見えるか</b>です。</p>
      <p class="muted">見方は2つあります。<b>記録された対局</b>では、終わった対局を再生します。<b>エージェントと対戦</b>（ヒューマンモード）では、あなたが相手側になり、エージェントは同じようにトレースされ、評価されます。</p>`,
      },
      notation: {
        title: "手の読み方",
        note: "1. e4 e5 2. Nf3 のあとです。黄色は直前の手です。丸で囲んだマスはf3です。",
        body: () => `
      <p>このアプリは、手を短い記法で書きます。<code>Nf3</code> は、ナイトがf3へ動いたことを表します。丸で囲んだマスが<b>f3</b>です。</p>
      <p>最初の文字が駒です。<code>N</code> はナイト、<code>B</code> はビショップ、<code>R</code> はルーク、<code>Q</code> はクイーン、<code>K</code> はキングです。ポーンには文字がありません。残りは、動き先のマスです。</p>
      <p>そのほかの印: <code>x</code> は駒を取ること（<code>Nxg5</code>）、<code>O-O</code> と <code>O-O-O</code> はキャスリング、<code>+</code> はチェック、<code>#</code> はチェックメイト、<code>=Q</code> はクイーンへのプロモーションです。</p>
      <p class="muted">エージェントが黒のときは、エージェント側が下になるように盤を回します。</p>`,
      },
      trace: {
        title: "1手に1つのトレース",
        note: "エージェントは、これから8手目を求められます。",
        body: () => `
      <p>エージェントが指すたびに、1つのトレースが記録されます。これは、サンプルの8手目です。</p>
      <ul class="tree" aria-label="8手目のトレース">
        <li><span class="n">chess.move</span> <span class="t">root · 7.9 s</span><span class="w">1手全体: ply、FEN、指せる手の数、そして後から付く評価。</span>
          <ul>
            <li><span class="n">invoke_agent chess-agent</span> <span class="t">6.8 s</span><span class="w">判断そのもの。この長さがレイテンシ（遅延）です。</span>
              <ul>
                <li><span class="n">chat sample-model</span> <span class="t">2.1 s</span><span class="w">モデルへの1回の呼び出し: 入力と出力のトークン、終了理由。</span></li>
                <li><span class="n">execute_tool legal_moves</span> <span class="t">3 ms</span><span class="w">モデルがツールを求めました。</span></li>
                <li><span class="n">chat sample-model</span> <span class="t">2.4 s</span></li>
                <li class="err"><span class="n">chess.validate_move</span> <span class="t">illegal_move</span><span class="w">モデルは <code>Nxg6</code> を提案しましたが、ここでは反則です。</span></li>
                <li><span class="n">chat sample-model</span> <span class="t">2.3 s</span><span class="w">やり直しです。</span></li>
                <li><span class="n">chess.validate_move</span> <span class="t">legal</span><span class="w">候補の <code>Nxg5</code> は正しい手なので、指されます。</span></li>
              </ul>
            </li>
            <li><span class="n">chess.evaluate</span> <span class="t">1.1 s</span><span class="w">Stockfishの評価。判断と同じ階層にあるので、この時間はレイテンシに含まれません。</span></li>
          </ul>
        </li>
      </ul>
      <p class="muted"><code>chat</code>、<code>invoke_agent</code>、<code>execute_tool</code> のスパンは、OpenTelemetryのGenAIセマンティック規約（<code>gen_ai.*</code>）に従っています。<code>chess.</code> と <code>gambit.</code> で始まる名前は、このプロジェクト独自のものです。このサンプルの時間は作った例です。</p>`,
      },
      numbers: {
        title: "1手の数値",
        note: "前のステップと同じ局面です。オレンジ色の表示は、やり直しや追加の呼び出しを表します。",
        body: (c) => `
      <p>リプレイは、トレースをいくつかの数値にまとめます。序盤の手と、8手目を比べてみましょう。</p>
      <div class="rowlabel">3手目</div>${c.chips(c.a3)}
      <div class="rowlabel">8手目</div>${c.chips(c.a8)}
      <dl class="keys">
        <dt>latency（遅延）</dt><dd>判断にかかった時間。<code>invoke_agent</code> の長さです。</dd>
        <dt>chat / tool calls</dt><dd>1回の判断の中の、モデル呼び出しとツール呼び出しの回数: <code>gambit.chat_calls</code>、<code>gambit.tool_calls</code>。</dd>
        <dt>in / out tok</dt><dd>すべての呼び出しを合計したトークン数: <code>gen_ai.usage.input_tokens</code>、<code>gen_ai.usage.output_tokens</code>。</dd>
        <dt>illegal tries（反則の試行）</dt><dd>提案された、反則の手の数: <code>chess.illegal_attempts</code>。3回やり直しても失敗すると、エージェントはランダムな合法手を指し、<code>chess.fallback</code> を立てます。</dd>
      </dl>`,
        chipNames: { latency: "遅延", chat: "chat呼び出し", inTok: "入力トークン", outTok: "出力トークン", tool: "tool呼び出し", illegal: "反則の試行" },
      },
      grading: {
        title: "ブランダーの評価の仕方",
        note: "赤は指された手です。緑の枠は、Stockfishが選んだはずのマスです: Bg3。",
        body: (c) => `
      <p>手のあと、Stockfishは、その前と後の局面を、決まった深さで採点します。評価は、<b>勝つ確率</b>がどれだけ下がったかです。</p>
      <div class="grades">
        <div><span class="cls" style="background:var(--best)">best</span><span class="muted">Stockfishの最善手と同じ</span></div>
        <div><span class="cls" style="background:var(--good)">good</span><span class="muted">下がった点が5点未満</span></div>
        <div><span class="cls" style="background:var(--inaccuracy)">inaccuracy</span><span class="muted">5点以上</span></div>
        <div><span class="cls" style="background:var(--mistake)">mistake</span><span class="muted">10点以上</span></div>
        <div><span class="cls" style="background:var(--blunder)">blunder</span><span class="muted">15点以上</span></div>
      </div>
      <p><b>8手目 Nxg5:</b> 評価値は ${c.fmtCP(c.a8.eval_before_cp)} から ${c.fmtCP(c.a8.eval_after_cp)} になり、勝つ確率は約 ${(c.a8.win_chance_drop * 50).toFixed(0)} 点下がりました。これは <span class="cls" style="background:var(--blunder)">blunder</span> です。ナイトはポーンを取りましたが、そのあとhポーンに取られます（<code>hxg5</code>）。</p>
      <p class="muted">評価は、トレースに <code>chess.move.class</code> として、評価値と並べて保存されます。また、<code>gen_ai.evaluation.result</code> イベントとしても送られます。</p>`,
      },
      predict: {
        title: "テレメトリからブランダーを予測する",
        note: "エージェントは、これから8手目を求められます。",
        body: (c) => `
      <p>評価は、手を指した<i>あと</i>にしか出ません。この講演の問いは、トレースがもっと早く警告を出せるかどうかです。小さなモデルが、遅延、トークン、やり直し、ツール呼び出しなどのトレースの特徴から、ブランダーのリスクを採点します。</p>
      <ul>
        <li><b style="color:var(--risk)">Forecast（予測）</b>は、局面と、<i>前の</i>手のトレースだけを使います。エージェントに手を求める前に、警告を出せます。</li>
        <li><b style="color:var(--risk2)">At-move（その手の時点）</b>は、その手自身のトレースも見ます。だから、指される前にその手を止められます。</li>
      </ul>
      ${c.timeline()}
      <div class="legend"><span style="color:var(--risk)">Forecast</span><span style="color:var(--risk2)">At-move</span><span style="color:var(--muted)">警告のしきい値</span></div>
      <div class="callout alert"><b>これから: エージェントの8手目</b><div style="font-size:20px;font-weight:700;margin-top:4px;color:var(--blunder)">リスク ${(c.a8.risk.forecast * 100).toFixed(0)}% · 手を求める前の警告</div></div>
      <p class="muted" style="margin-top:10px">このリスクの数値は、サンプルのために作った例です。リプレイが描くものを示しているだけで、実際の結果ではありません。</p>`,
        timelineLabel: "手ごとのリスクのサンプル: 予測は、ブランダーの8手目で警告のしきい値を超えます",
        timelineMove: "{n}. {san} · {cls}",
      },
      try: {
        title: "自分で試す",
        note: "8… hxg5 のあとのサンプル対局です。",
        body: (c) => `
      <p>では、本物を試しましょう。</p>
      <div class="ctas">
        <a class="btn primary" href="index.html">${c.ui.tabRecorded} ▸</a>
        <a class="btn ${c.live ? "primary" : "off"}" href="live.html" ${c.live ? "" : 'aria-disabled="true" tabindex="-1"'}>${c.ui.tabLive} ▸</a>
      </div>
      <p class="muted">${c.live ? "このサーバーではヒューマンモードが有効です。あなたが片方を指し、エージェントがもう片方を指します。" : "このサーバーではヒューマンモードが無効です。エージェントと対戦するには、<code>gambit serve --live</code> で起動してください。"}</p>
      <ul>
        <li>リプレイでは、<kbd>b</kbd> キーで次のブランダーへ移れます。タイムラインの点をクリックしても移れます。</li>
        <li><kbd>←</kbd> <kbd>→</kbd> で手を進めながら、エージェントの手の前のリスクバーを見ましょう。</li>
        <li><b>Open trace ↗</b> を押すと、その手の完全なトレースへ移れます。</li>
        <li><b>エージェントと対戦</b>では、コーチがあなたの手ごとにコメントします。どの手でも <b>Ask the coach why</b> を押すと、理由を聞けます。</li>
      </ul>
      <p class="muted">ルールをもう一度見たいときは、<button class="linkbtn" data-goto="part1">パート1</button>に戻りましょう。</p>`,
      },
    },
  };

  const TEXT = { en, ja };
  const api = { TEXT, PIECES, pieceLabel, pieceShort };
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  else root.TutorialText = api;
})(typeof window !== "undefined" ? window : globalThis);
