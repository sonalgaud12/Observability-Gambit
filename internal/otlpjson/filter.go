// Package otlpjson filters the Collector's OTLP-JSON file output by trace ID,
// so a single game can be cut out of a big recording and replayed on stage.
package otlpjson

import (
	"bufio"
	"encoding/json"
	"io"
)

// Filter copies OTLP-JSON lines from r to w, keeping only spans whose traceId
// is in keep. Empty scopes and resources are dropped. It returns the number of
// spans written.
func Filter(r io.Reader, w io.Writer, keep map[string]bool) (int, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 256<<20)
	enc := json.NewEncoder(w)
	n := 0
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal(sc.Bytes(), &doc); err != nil {
			return n, err
		}
		rss, _ := doc["resourceSpans"].([]any)
		var keptRS []any
		for _, rsAny := range rss {
			rs, _ := rsAny.(map[string]any)
			sss, _ := rs["scopeSpans"].([]any)
			var keptSS []any
			for _, ssAny := range sss {
				ss, _ := ssAny.(map[string]any)
				spans, _ := ss["spans"].([]any)
				var keptSpans []any
				for _, spAny := range spans {
					sp, _ := spAny.(map[string]any)
					if id, _ := sp["traceId"].(string); keep[id] {
						keptSpans = append(keptSpans, sp)
					}
				}
				if len(keptSpans) > 0 {
					ss["spans"] = keptSpans
					keptSS = append(keptSS, ss)
					n += len(keptSpans)
				}
			}
			if len(keptSS) > 0 {
				rs["scopeSpans"] = keptSS
				keptRS = append(keptRS, rs)
			}
		}
		if len(keptRS) == 0 {
			continue
		}
		if err := enc.Encode(map[string]any{"resourceSpans": keptRS}); err != nil {
			return n, err
		}
	}
	return n, sc.Err()
}
