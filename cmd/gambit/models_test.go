package main

import (
	"flag"
	"strings"
	"testing"
	"time"

	"github.com/sonalgaud12/observability-gambit/internal/llm"
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

func TestAnthropicProviderUsesTheNativeClient(t *testing.T) {
	t.Setenv("GAMBIT_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("ANTHROPIC_API_KEY", "sk-anthropic")
	m := newFlags(t, "--provider", "anthropic", "--effort", "high")
	if !m.native() || m.apiKey() != "sk-anthropic" {
		t.Errorf("native = %v, key = %q (an OpenAI key must never be sent to Anthropic)", m.native(), m.apiKey())
	}
	c, ok := newClient("anthropic", *m.baseURL, "k", *m.effort, 0).(*llm.Anthropic)
	if !ok || c.Effort != "high" || c.HTTP.Timeout != 10*time.Minute || c.BaseURL != "https://api.anthropic.com" {
		t.Errorf("client = %+v", c)
	}
	if c := newClient("anthropic", "", "k", "", 42*time.Second).(*llm.Anthropic); c.HTTP.Timeout != 42*time.Second {
		t.Errorf("timeout = %v", c.HTTP.Timeout)
	}
	// Everything else stays on the OpenAI-compatible client, with its old defaults.
	o, ok := newClient("openai", "", "k", "", 0).(*llm.OpenAICompatible)
	if !ok || o.BaseURL != "https://api.openai.com/v1" || o.HTTP.Timeout != 3*time.Minute || o.ReasoningEffort != "" {
		t.Errorf("client = %+v", o)
	}
	if _, ok := newClient("ollama", "http://localhost:11434/v1", "", "", 0).(*llm.OpenAICompatible); !ok {
		t.Error("ollama is not OpenAI-compatible")
	}
	if m := newFlags(t, "--provider", "anthropic", "--mock"); m.native() {
		t.Error("--mock must stay offline")
	}
}

func TestTemperatureDefaultsToTheProviderForClaude(t *testing.T) {
	parse := func(args ...string) float64 {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		m := registerModelFlags(fs, "model")
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		m.finish(fs)
		return *m.temperature
	}
	if got := parse("--provider", "anthropic"); got >= 0 {
		t.Errorf("anthropic default = %v", got)
	}
	if got := parse("--provider", "anthropic", "--temperature", "1"); got != 1 {
		t.Errorf("explicit = %v", got)
	}
	if got := parse(); got != 0.7 {
		t.Errorf("openai default changed: %v", got)
	}
}

func TestEffortFlagTunesClaudeButNotLuna(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-o")
	t.Setenv("ANTHROPIC_API_KEY", "sk-a")
	cs, err := newFlags(t, "--effort", "low", "--llm-timeout", "5m").modelChoices([]string{"haiku-5.5", "luna-6"})
	if err != nil {
		t.Fatal(err)
	}
	h := cs[0].NewClient(1, nil).(*llm.Instrumented).Inner.(*llm.Anthropic)
	if h.Effort != "low" || h.HTTP.Timeout != 5*time.Minute {
		t.Errorf("haiku client = %+v", h)
	}
	l := cs[1].NewClient(1, nil).(*llm.Instrumented).Inner.(*llm.OpenAICompatible)
	if l.ReasoningEffort != "none" || l.HTTP.Timeout != 5*time.Minute {
		t.Errorf("luna client = %+v", l)
	}
}

func TestAnthropicKeyIsNotSentToOpenAI(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-a")
	if err := newFlags(t, "--provider", "anthropic", "--base-url", "https://api.openai.com/v1").check(); err == nil {
		t.Error("an Anthropic key would go to api.openai.com")
	}
	if err := newFlags(t, "--provider", "anthropic").check(); err != nil {
		t.Errorf("default anthropic setup rejected: %v", err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GAMBIT_API_KEY", "")
	err := newFlags(t, "--provider", "anthropic").check()
	if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("err = %v", err)
	}
}

func TestMoveTimeoutFollowsClaudeEffort(t *testing.T) {
	parse := func(args ...string) *modelFlags { return newFlags(t, args...) }
	for _, tc := range []struct {
		args []string
		ids  []string
		flag string
		want time.Duration
	}{
		{nil, nil, "auto", 15 * time.Second},                                 // the default model answers directly
		{[]string{"--provider", "anthropic"}, nil, "auto", 30 * time.Second}, // default effort is medium
		{[]string{"--provider", "anthropic", "--effort", "low"}, nil, "auto", 20 * time.Second},
		{[]string{"--provider", "anthropic", "--effort", "high"}, nil, "auto", 60 * time.Second},
		{[]string{"--provider", "anthropic", "--effort", "max"}, nil, "auto", 120 * time.Second},
		{nil, []string{"luna-6"}, "auto", 15 * time.Second},
		{nil, []string{"haiku-5.5", "luna-6"}, "auto", 30 * time.Second},
		{[]string{"--provider", "anthropic", "--mock"}, nil, "auto", 15 * time.Second},
		{[]string{"--provider", "anthropic"}, nil, "45s", 45 * time.Second}, // explicit wins
		{nil, nil, "0", 0},
	} {
		got, err := parse(tc.args...).moveTimeout(tc.flag, tc.ids)
		if err != nil || got != tc.want {
			t.Errorf("%v %v %s: got %v, %v; want %v", tc.args, tc.ids, tc.flag, got, err, tc.want)
		}
	}
	for _, bad := range []string{"soon", "-5s", ""} {
		if _, err := parse().moveTimeout(bad, nil); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
