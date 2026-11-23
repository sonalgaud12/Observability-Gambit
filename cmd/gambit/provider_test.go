package main

import (
	"flag"
	"testing"
	"time"

	"github.com/sonalgaud12/observability-gambit/internal/llm"
)

func parseFlags(t *testing.T, args ...string) *modelFlags {
	t.Helper()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	m := registerModelFlags(fs, "model")
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	m.finish(fs)
	return m
}

// --provider openai-responses builds the Responses client, which reports
// itself as "openai"; every other provider value stays on Chat Completions.
func TestNewClientPicksTheAPI(t *testing.T) {
	c := newClient("openai-responses", "https://api.openai.com/v1", "k", "low", 0)
	r, ok := c.(*llm.OpenAIResponses)
	if !ok || r.Effort != "low" || r.Provider() != "openai" {
		t.Fatalf("client = %#v", c)
	}
	if r.HTTP.Timeout != llm.DefaultResponsesTimeout {
		t.Errorf("default timeout = %v", r.HTTP.Timeout)
	}
	c = newClient("openai-responses", "", "k", "", 45*time.Second)
	if c.(*llm.OpenAIResponses).HTTP.Timeout != 45*time.Second {
		t.Errorf("--llm-timeout was ignored")
	}

	for _, p := range []string{"openai", "ollama"} {
		c := newClient(p, "http://localhost:11434/v1", "", "", 0)
		o, ok := c.(*llm.OpenAICompatible)
		if !ok || o.Provider() != p || o.HTTP.Timeout != 3*time.Minute {
			t.Errorf("%s: client = %#v", p, c)
		}
	}
	o := newClient("openai", "https://api.openai.com/v1", "k", "medium", 20*time.Minute).(*llm.OpenAICompatible)
	if o.ReasoningEffort != "medium" || o.HTTP.Timeout != 20*time.Minute {
		t.Errorf("chat client = %#v", o)
	}
}

// Reasoning models reject a temperature and need room to think, so the
// Responses provider changes those two defaults, but only when not set.
func TestResponsesProviderDefaults(t *testing.T) {
	m := parseFlags(t, "--provider", "openai-responses")
	if *m.temperature >= 0 || *m.maxTokens != 4096 {
		t.Errorf("temperature %v, max tokens %d", *m.temperature, *m.maxTokens)
	}
	m = parseFlags(t, "--provider", "openai-responses", "--temperature", "0.2", "--max-tokens", "2000")
	if *m.temperature != 0.2 || *m.maxTokens != 2000 {
		t.Errorf("explicit values changed: temperature %v, max tokens %d", *m.temperature, *m.maxTokens)
	}
	m = parseFlags(t, "--provider", "openai")
	if *m.temperature != 0.7 || *m.maxTokens != 1024 {
		t.Errorf("chat defaults changed: temperature %v, max tokens %d", *m.temperature, *m.maxTokens)
	}
	m = parseFlags(t, "--provider", "openai-responses", "--mock")
	if *m.temperature != 0.7 {
		t.Errorf("--mock changed the temperature to %v", *m.temperature)
	}
}

// Reasoning counts against the per-move cap, so "auto" gives openai-responses
// the same effort-scaled cap as Claude, and a plain OpenAI model the flat 15s.
func TestMoveTimeoutAutoForResponses(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want time.Duration
	}{
		{[]string{"--provider", "openai-responses"}, 30 * time.Second},
		{[]string{"--provider", "openai-responses", "--effort", "low"}, 20 * time.Second},
		{[]string{"--provider", "openai-responses", "--effort", "high"}, 60 * time.Second},
		{[]string{"--provider", "openai-responses", "--mock"}, 15 * time.Second},
		{[]string{"--provider", "openai"}, 15 * time.Second},
	} {
		got, err := parseFlags(t, tc.args...).moveTimeout("auto", nil)
		if err != nil || got != tc.want {
			t.Errorf("%v: move timeout %v, %v; want %v", tc.args, got, err, tc.want)
		}
	}
}
