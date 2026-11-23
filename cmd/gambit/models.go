package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/sonalgaud12/observability-gambit/internal/agent"
	"github.com/sonalgaud12/observability-gambit/internal/game"
	"github.com/sonalgaud12/observability-gambit/internal/llm"
)

// preset is a model a shared deployment may offer. The allow-list for
// `serve --live --models` can only name presets, so a player can never reach
// a model (or a price) the operator did not list.
type preset struct {
	Label       string
	Provider    string // gen_ai.provider.name
	BaseURL     string
	Model       string
	KeyEnv      string // environment variable holding this provider's API key
	Temperature float64
	MaxTokens   int

	// Effort is the reasoning effort sent to the model: output_config.effort
	// for the native Anthropic client, reasoning.effort for the Responses
	// client ("" = the model's default).
	Effort string

	// Responses selects OpenAI's native Responses API instead of Chat
	// Completions, so the model's reasoning summary reaches the trace.
	Responses bool
}

// presets verified against the providers' documentation on 2026-11-17:
//   - claude-haiku-5-5 over Anthropic's native Messages API, so the thinking
//     summary and thinking-token count reach the trace (the OpenAI-compatible
//     endpoint returns neither). Temperature is left to the provider (Claude 5
//     models reject other values) and the output cap leaves room for thinking.
//   - gpt-6-luna: developers.openai.com/api/docs/models/gpt-6-luna lists the
//     single id gpt-6-luna and says function calling with reasoning needs the
//     Responses API (Chat Completions supports it only at effort "none", which
//     returns no reasoning). So it uses the Responses client at effort "low":
//     reasoning.effort accepts none, low, medium (default), high, xhigh, max,
//     and low is the smallest effort that still produces a reasoning summary.
var presets = map[string]preset{
	"haiku-5.5": {
		Label: "Claude Haiku 5.5", Provider: "anthropic", BaseURL: "https://api.anthropic.com",
		Model: "claude-haiku-5-5", KeyEnv: "ANTHROPIC_API_KEY", Temperature: -1, MaxTokens: 4096,
	},
	"luna-6": {
		Label: "GPT-6 Luna", Provider: "openai", BaseURL: "https://api.openai.com/v1",
		Model: "gpt-6-luna", KeyEnv: "OPENAI_API_KEY", Temperature: -1, MaxTokens: 4096,
		Effort: "low", Responses: true,
	},
}

// parseModelList turns "haiku-5.5,luna-6" into preset IDs, rejecting anything
// that is not a known preset and any repeat.
func parseModelList(s string) ([]string, error) {
	var ids []string
	seen := map[string]bool{}
	for _, id := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if _, ok := presets[id]; !ok {
			return nil, fmt.Errorf("--models: unknown model %q (known: %s)", id, knownPresets())
		}
		if seen[id] {
			return nil, fmt.Errorf("--models: %q listed twice", id)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("--models is empty (known: %s)", knownPresets())
	}
	return ids, nil
}

func knownPresets() string {
	return "haiku-5.5, luna-6"
}

// modelChoices builds the live allow-list. In mock mode every choice uses the
// offline mock and is named "mock-<model id>", so the model on the trace still
// shows which option the player picked and can never pass for a real result.
func (m *modelFlags) modelChoices(ids []string) ([]game.ModelChoice, error) {
	var out []game.ModelChoice
	for _, id := range ids {
		p := presets[id]
		key := os.Getenv(p.KeyEnv)
		if !*m.mock && key == "" {
			return nil, fmt.Errorf("model %s needs %s to be set (or drop it from --models)", id, p.KeyEnv)
		}
		name := p.Model
		if *m.mock {
			name = "mock-" + p.Model
		}
		out = append(out, game.ModelChoice{
			ID: id, Label: p.Label,
			Agent: agent.Config{
				Model: name, Temperature: p.Temperature, MaxTokens: p.MaxTokens, MaxRetries: *m.retries,
				LegalMovesInPrompt: *m.legalInPrompt, Tools: *m.tools,
			},
			NewClient: func(s int64, brain llm.Brain) llm.Client {
				if *m.mock {
					mm := llm.NewMock(s)
					mm.Brain = brain
					return llm.Instrument(mm)
				}
				if p.Provider == "anthropic" {
					effort := p.Effort
					if *m.effort != "" { // --effort tunes the Claude options; GPT-6 Luna keeps its own
						effort = *m.effort
					}
					return llm.Instrument(newClient(p.Provider, p.BaseURL, key, effort, *m.llmTimeout))
				}
				if p.Responses {
					return llm.Instrument(newClient("openai-responses", p.BaseURL, key, p.Effort, *m.llmTimeout))
				}
				return llm.Instrument(newClient(p.Provider, p.BaseURL, key, p.Effort, *m.llmTimeout))
			},
		})
	}
	return out, nil
}
