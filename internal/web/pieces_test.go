package web

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

var (
	pieceKinds  = []string{"K", "Q", "R", "B", "N", "P"}
	pieceColors = []string{"w", "b"}
)

func svgFile(t *testing.T, base, name string) (ctype, body string) {
	t.Helper()
	resp, err := http.Get(base + "/pieces/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/pieces/%s: status %d", name, resp.StatusCode)
	}
	_, body = getBody(t, base+"/pieces/"+name)
	return resp.Header.Get("Content-Type"), body
}

// Every piece in both colours is bundled, served as an image, and is well-formed SVG
// with real artwork. No two files are the same, and the two sides differ in fill AND outline,
// so sides never rely on hue alone.
func TestEveryPieceAndColourRenders(t *testing.T) {
	srv := replayOnly(t)
	seen := map[string]string{}
	for _, c := range pieceColors {
		for _, k := range pieceKinds {
			name := c + k + ".svg"
			ctype, body := svgFile(t, srv.URL, name)
			if !strings.HasPrefix(ctype, "image/svg+xml") {
				t.Errorf("%s: content type %q, want image/svg+xml", name, ctype)
			}
			dec := xml.NewDecoder(strings.NewReader(body))
			var viewBox string
			shapes := 0
			for {
				tok, err := dec.Token()
				if err != nil {
					break
				}
				if se, ok := tok.(xml.StartElement); ok {
					if se.Name.Local == "svg" {
						for _, a := range se.Attr {
							if a.Name.Local == "viewBox" {
								viewBox = a.Value
							}
						}
					} else {
						shapes++
					}
				}
			}
			if _, err := xml.NewDecoder(strings.NewReader(body)).Token(); err != nil {
				t.Errorf("%s: not XML: %v", name, err)
			}
			if viewBox != "0 0 100 100" {
				t.Errorf("%s: viewBox %q, want \"0 0 100 100\"", name, viewBox)
			}
			if shapes < 4 {
				t.Errorf("%s: only %d shapes, want real artwork (halo, outline, bars)", name, shapes)
			}
			if strings.Contains(body, "http://") && !strings.Contains(body, "http://www.w3.org/2000/svg") || strings.Contains(body, "https://") || strings.Contains(body, "<image") || strings.Contains(body, "<text") {
				t.Errorf("%s: must be self-contained vector art (no external refs, no text)", name)
			}
			if prev, dup := seen[body]; dup {
				t.Errorf("%s is identical to %s", name, prev)
			}
			seen[body] = name
		}
	}
	for _, k := range pieceKinds {
		_, w := svgFile(t, srv.URL, "w"+k+".svg")
		_, b := svgFile(t, srv.URL, "b"+k+".svg")
		// white: pale fill + dark halo/outline; black: dark fill + light halo.
		if !strings.Contains(w, `fill="#eefbff"`) || !strings.Contains(b, `fill="#1b2230"`) || !strings.Contains(b, `stroke="#f4f7fb"`) {
			t.Errorf("%s: sides must differ by fill and outline, not hue alone", k)
		}
	}
	if len(seen) != 12 {
		t.Errorf("%d distinct pieces, want 12", len(seen))
	}
}

// runOG executes the served og.js in node and returns the JSON the script prints.
func runOG(t *testing.T, base, script string, out any) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available")
	}
	_, js := getBody(t, base+"/og.js")
	cmd := exec.Command(node, "-e", `const OG = new Function(require("fs").readFileSync(0, "utf8") + ";return OG;")();
console.log(JSON.stringify((function(){`+script+`})()));`)
	cmd.Stdin = strings.NewReader(js)
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("bad output %q: %v", b, err)
	}
}

// OG.pc draws an img for every FEN letter, pointing at a bundled piece file that is served.
func TestPieceHelperDrawsEveryFenLetter(t *testing.T) {
	srv := replayOnly(t)
	var got map[string]string
	runOG(t, srv.URL, `const o = {}; for (const ch of "KQRBNPkqrbnp") o[ch] = OG.pc(ch); return o;`, &got)
	if len(got) != 12 {
		t.Fatalf("got %d pieces, want 12", len(got))
	}
	src := regexp.MustCompile(`<img[^>]* src="(pieces/[^"]+\.svg)"`)
	for ch, html := range got {
		m := src.FindStringSubmatch(html)
		if m == nil {
			t.Errorf("%s: no piece img in %q", ch, html)
			continue
		}
		color := "b"
		if ch == strings.ToUpper(ch) {
			color = "w"
		}
		if want := "pieces/" + color + strings.ToUpper(ch) + ".svg"; m[1] != want {
			t.Errorf("%s: img %s, want %s", ch, m[1], want)
		}
		if resp, err := http.Get(srv.URL + "/" + m[1]); err != nil || resp.StatusCode != 200 {
			t.Errorf("%s: %s not served", ch, m[1])
		} else {
			resp.Body.Close()
		}
	}
}

// OG.checkSquare names the king in check, for either side to move, and only for + or # moves.
func TestCheckSquareFindsKing(t *testing.T) {
	srv := replayOnly(t)
	var got []*string
	runOG(t, srv.URL, `return [
		OG.checkSquare("4k3/8/8/8/8/8/8/4K2R w - - 0 1", "Rh8+"),
		OG.checkSquare("4k3/8/8/8/8/8/8/4K2R b - - 0 1", "Rh8#"),
		OG.checkSquare("4k3/8/8/8/8/8/8/4K2R b - - 0 1", "Rh7"),
		OG.checkSquare("", "Rh8+"),
		OG.checkSquare("rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1", "e4+"),
		OG.checkSquare("rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR b KQkq - 0 1", "Nf3+"),
	];`, &got)
	want := []string{"e1", "e8", "", "", "e1", "e8"}
	for i, w := range want {
		switch {
		case w == "" && got[i] != nil:
			t.Errorf("case %d: got %q, want none", i, *got[i])
		case w != "" && (got[i] == nil || *got[i] != w):
			t.Errorf("case %d: got %v, want %s", i, got[i], w)
		}
	}
}

// OG.tag renders the word on the square, with its position class.
func TestTagRendersWord(t *testing.T) {
	srv := replayOnly(t)
	var got string
	runOG(t, srv.URL, `return OG.tag("BLUNDER", "tl");`, &got)
	if got != `<span class="tag tl">BLUNDER</span>` {
		t.Errorf("tag = %q", got)
	}
}

// The pages ship no Unicode glyph pieces and pull nothing from outside the bundle.
func TestPagesBundledAndGlyphFree(t *testing.T) {
	srv := replayOnly(t)
	glyphs := regexp.MustCompile("[♔-♟]")
	ext := regexp.MustCompile(`(?:src|href)="(https?://[^"]+)"`)
	for _, p := range []string{"/index.html", "/live.html", "/tutorial.html"} {
		_, page := getBody(t, srv.URL+p)
		if glyphs.MatchString(page) {
			t.Errorf("%s: still has Unicode chess glyphs", p)
		}
		for _, u := range ext.FindAllStringSubmatch(page, -1) {
			t.Errorf("%s: external resource %s (everything must be bundled)", p, u[1])
		}
	}
}
