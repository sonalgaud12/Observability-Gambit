package main

import (
	"flag"
	"strings"
	"testing"
)

func TestParseModelList(t *testing.T) {
	ids, err := parseModelList("haiku-5.5, luna-6")
	if err != nil || len(ids) != 2 || ids[0] != "haiku-5.5" || ids[1] != "luna-6" {
		t.Fatalf("ids = %v, err = %v", ids, err)
	}
	for _, bad := range []string{"", "gpt-4o", "luna-6,luna-6", "haiku-5.5,sonnet-5.5"} {
		if _, err := parseModelList(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func newFlags(t *testing.T, args ...string) *modelFlags {
	t.Helper()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	m := registerModelFlags(fs, "agent-model")
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return m
}

// Both options exist offline: no keys, mock clients, model names that say mock.
func TestModelChoicesInMockMode(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	cs, err := newFlags(t, "--mock").modelChoices([]string{"haiku-5.5", "luna-6"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[0].Agent.Model != "mock-claude-haiku-5-5" || cs[1].Agent.Model != "mock-gpt-6-luna" {
		t.Errorf("choices = %+v", cs)
	}
	if p := cs[0].NewClient(1, nil).Provider(); p != "mock" {
		t.Errorf("provider = %q", p)
	}
}

func TestModelChoicesNeedTheirOwnKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	_, err := newFlags(t).modelChoices([]string{"haiku-5.5", "luna-6"})
	if err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") || strings.Contains(err.Error(), "sk-test") {
		t.Fatalf("err = %v", err)
	}
	cs, err := newFlags(t).modelChoices([]string{"haiku-5.5"}) // one model offered, one key needed
	if err != nil {
		t.Fatal(err)
	}
	if cs[0].Agent.Model != "claude-haiku-5-5" || cs[0].Agent.Temperature >= 0 || cs[0].Agent.MaxTokens != 4096 {
		t.Errorf("haiku preset = %+v", cs[0].Agent)
	}
	if p := cs[0].NewClient(1, nil).Provider(); p != "anthropic" {
		t.Errorf("provider = %q", p)
	}
}
