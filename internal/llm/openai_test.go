package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// capture serves one canned completion and returns the request body it saw.
func capture(t *testing.T, c *OpenAICompatible) map[string]any {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &got); err != nil {
			t.Errorf("bad body: %v", err)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		io.WriteString(w, `{"id":"x","model":"m","choices":[{"message":{"role":"assistant","content":"e4"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	}))
	defer srv.Close()
	c.BaseURL, c.APIKey = srv.URL, "k"
	resp, err := c.Chat(context.Background(), Request{Model: "m", Temperature: -1, MaxTokens: 4096,
		Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.InputTokens != 3 || resp.OutputTokens != 2 {
		t.Errorf("usage = %+v", resp)
	}
	return got
}

// The Anthropic preset: plain chat completions, max_tokens, nothing extra.
func TestRequestDefaultsAreUnchanged(t *testing.T) {
	got := capture(t, NewOpenAICompatible("", "", "anthropic"))
	if got["max_tokens"] != float64(4096) {
		t.Errorf("max_tokens = %v", got["max_tokens"])
	}
	for _, k := range []string{"max_completion_tokens", "reasoning_effort", "temperature"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s was sent by default: %v", k, got[k])
		}
	}
}

// The OpenAI GPT-6 preset: max_completion_tokens and reasoning_effort none
// (the only setting that allows function calling on Chat Completions).
func TestRequestForOpenAIReasoningModels(t *testing.T) {
	c := NewOpenAICompatible("", "", "openai")
	c.ReasoningEffort, c.MaxCompletionTokens = "none", true
	got := capture(t, c)
	if got["max_completion_tokens"] != float64(4096) || got["reasoning_effort"] != "none" {
		t.Errorf("body = %v", got)
	}
	if _, ok := got["max_tokens"]; ok {
		t.Errorf("max_tokens was sent alongside max_completion_tokens: %v", got)
	}
}
