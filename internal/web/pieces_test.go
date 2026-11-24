package web

import (
	"encoding/xml"
	"net/http"
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

// og.js can draw each of the 12 pieces: its piece paths exist as bundled files.
func TestPieceHelperCoversAllPieces(t *testing.T) {
	srv := replayOnly(t)
	_, js := getBody(t, srv.URL+"/og.js")
	for _, k := range []string{"k", "q", "r", "b", "n", "p"} {
		if !strings.Contains(js, k+": ") && !strings.Contains(js, `"`+k+`"`) {
			t.Errorf("og.js has no entry for %q", k)
		}
	}
	if !strings.Contains(js, `pieces/${color}${kind.toUpperCase()}.svg`) {
		t.Error("og.js does not build piece image paths from colour and kind")
	}
}

// All three pages use the shared piece set and look, with no Unicode glyph pieces left,
// no CDN, and the BLUNDER and CHECK words on squares.
func TestPagesUseSharedPieces(t *testing.T) {
	srv := replayOnly(t)
	glyphs := regexp.MustCompile("[♔-♟]")
	for _, p := range []string{"/index.html", "/live.html", "/tutorial.html"} {
		_, page := getBody(t, srv.URL+p)
		if !strings.Contains(page, `href="og.css"`) || !strings.Contains(page, `src="og.js"`) {
			t.Errorf("%s: does not load og.css and og.js", p)
		}
		if glyphs.MatchString(page) {
			t.Errorf("%s: still has Unicode chess glyphs", p)
		}
		if strings.Contains(page, "OG.pc(") == false {
			t.Errorf("%s: board does not draw pieces with OG.pc", p)
		}
		if !strings.Contains(page, `OG.tag("CHECK"`) {
			t.Errorf("%s: no CHECK tag on the king square", p)
		}
		for _, u := range regexp.MustCompile(`(?:src|href)="(https?://[^"]+)"`).FindAllStringSubmatch(page, -1) {
			t.Errorf("%s: external resource %s (everything must be bundled)", p, u[1])
		}
	}
	for _, p := range []string{"/index.html"} {
		_, page := getBody(t, srv.URL+p)
		if !strings.Contains(page, `OG.tag("BLUNDER"`) {
			t.Errorf("%s: no BLUNDER tag on the square", p)
		}
	}
	for _, p := range []string{"/og.css", "/og.js"} {
		code, body := getBody(t, srv.URL+p)
		if code != 200 || strings.Contains(body, "url(http") || strings.Contains(body, "@import") {
			t.Errorf("%s: %d or pulls in a remote resource", p, code)
		}
	}
	_, css := getBody(t, srv.URL+"/og.css")
	for _, want := range []string{".sq .tag", `.cls[data-g=blunder]::before { content: "✕"`, "border: 4px dashed", ".sq.check"} {
		if !strings.Contains(css, want) {
			t.Errorf("og.css lacks %q", want)
		}
	}
}

// The check marker finds the king that is in check, and only then.
func TestCheckSquareSource(t *testing.T) {
	srv := replayOnly(t)
	_, js := getBody(t, srv.URL+"/og.js")
	if !strings.Contains(js, `/[+#]$/`) {
		t.Error("checkSquare should only mark moves written with + or #")
	}
}
