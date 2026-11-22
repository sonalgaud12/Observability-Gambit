package web

import (
	"encoding/json"
	"net/url"
	"strings"
)

// TraceIDPlaceholder is what the stage UI replaces with a move's trace ID.
const TraceIDPlaceholder = "{trace_id}"

// ExploreTraceURL returns a TraceURL template that opens a trace in Grafana
// Explore on the provisioned Tempo datasource (uid "tempo"). base is Grafana's
// root URL, e.g. http://localhost:3000.
//
// The query time range is the last 30 days, not Explore's default hour: Tempo
// uses the range to pick which blocks to read, so a trace older than the range
// reads as "trace not found" even though it is stored.
//
// The placeholder is left unescaped on purpose: the UI does a plain string
// replace, and trace IDs are hex, so they need no escaping.
func ExploreTraceURL(base string) string {
	panes := map[string]any{"a": map[string]any{
		"datasource": "tempo",
		"queries": []any{map[string]any{
			"refId":      "A",
			"datasource": map[string]string{"type": "tempo", "uid": "tempo"},
			"queryType":  "traceql",
			"query":      TraceIDPlaceholder,
		}},
		"range": map[string]string{"from": "now-30d", "to": "now"},
	}}
	b, _ := json.Marshal(panes)
	enc := url.QueryEscape(string(b))
	enc = strings.ReplaceAll(enc, url.QueryEscape(TraceIDPlaceholder), TraceIDPlaceholder)
	return strings.TrimRight(base, "/") + "/explore?schemaVersion=1&panes=" + enc + "&orgId=1"
}
