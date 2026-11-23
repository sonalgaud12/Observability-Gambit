package web

import (
	"encoding/json"
	"net/url"
	"strings"
)

// TraceIDPlaceholder is what the stage UI replaces with a move's trace ID.
const TraceIDPlaceholder = "{trace_id}"

// FromPlaceholder and ToPlaceholder are replaced with the start and end of the
// query time range, in epoch milliseconds.
const (
	FromPlaceholder = "{from}"
	ToPlaceholder   = "{to}"
)

// ExploreTraceURL returns a TraceURL template that opens a trace in Grafana
// Explore on the provisioned Tempo datasource (uid "tempo"). base is Grafana's
// root URL, e.g. http://localhost:3000.
//
// The query time range is a window around the move, not Explore's default
// hour: Tempo uses the range to pick which blocks to read, so a trace outside
// the range reads as "trace not found" even though it is stored. The UI
// replaces FromPlaceholder and ToPlaceholder with epoch milliseconds around the
// move's start time.
//
// The placeholders are left unescaped on purpose: the UI does a plain string
// replace, and trace IDs and epoch milliseconds need no escaping.
func ExploreTraceURL(base string) string {
	panes := map[string]any{"a": map[string]any{
		"datasource": "tempo",
		"queries": []any{map[string]any{
			"refId":      "A",
			"datasource": map[string]string{"type": "tempo", "uid": "tempo"},
			"queryType":  "traceql",
			"query":      TraceIDPlaceholder,
		}},
		"range": map[string]string{"from": FromPlaceholder, "to": ToPlaceholder},
	}}
	b, _ := json.Marshal(panes)
	enc := url.QueryEscape(string(b))
	for _, ph := range []string{TraceIDPlaceholder, FromPlaceholder, ToPlaceholder} {
		enc = strings.ReplaceAll(enc, url.QueryEscape(ph), ph)
	}
	return strings.TrimRight(base, "/") + "/explore?schemaVersion=1&panes=" + enc + "&orgId=1"
}
