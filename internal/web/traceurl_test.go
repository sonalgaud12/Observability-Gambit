package web

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestExploreTraceURL(t *testing.T) {
	tmpl := ExploreTraceURL("http://localhost:3000/")
	for _, ph := range []string{TraceIDPlaceholder, FromPlaceholder, ToPlaceholder} {
		if strings.Count(tmpl, ph) != 1 {
			t.Fatalf("want exactly one literal %s in %s", ph, tmpl)
		}
	}
	tmpl = strings.NewReplacer(FromPlaceholder, "1700000000000", ToPlaceholder, "1700010800000").Replace(tmpl)
	const id = "0123456789abcdef0123456789abcdef"
	u, err := url.Parse(strings.Replace(tmpl, TraceIDPlaceholder, id, 1))
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "localhost:3000" || u.Path != "/explore" {
		t.Fatalf("wrong target: %s", u)
	}
	var panes map[string]struct {
		Datasource string `json:"datasource"`
		Queries    []struct {
			Datasource struct{ Type, UID string } `json:"datasource"`
			QueryType  string                     `json:"queryType"`
			Query      string                     `json:"query"`
		} `json:"queries"`
		Range struct{ From, To string } `json:"range"`
	}
	if err := json.Unmarshal([]byte(u.Query().Get("panes")), &panes); err != nil {
		t.Fatalf("panes is not JSON: %v", err)
	}
	p := panes["a"]
	if len(p.Queries) != 1 || p.Queries[0].Query != id || p.Queries[0].Datasource.UID != "tempo" || p.Queries[0].QueryType != "traceql" {
		t.Fatalf("query does not look up the trace on the tempo datasource: %+v", p)
	}
	// Explore's default 1h range would hide any older trace from Tempo.
	if p.Range.From != "1700000000000" || p.Range.To != "1700010800000" {
		t.Fatalf("range %+v: want the window the UI filled in", p.Range)
	}
}
