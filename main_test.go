package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const codexRuleYAML = `
rules:
  - name: codex-identity-normalize
    mode: regex_replace
    source_formats:
      - openai-response
    scopes:
      - system
      - developer
    models:
      - gemini-*
    pattern: '(?i)(You are Codex,\s+[^.]+?)\s+based on\s+[^.]+(\.?)'
    replacement: '${1}${2}'
`

func mustConfig(t *testing.T, raw string) compiledConfig {
	t.Helper()
	cfg, err := compileConfig([]byte(raw))
	if err != nil {
		t.Fatalf("compileConfig() error = %v", err)
	}
	return cfg
}

func decodeJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	return out
}

func TestCodexIdentityRegexHandlesCurrentAndFutureVariants(t *testing.T) {
	cfg := mustConfig(t, codexRuleYAML)
	for _, tc := range []struct {
		in   string
		want string
	}{
		{
			in:   "You are Codex, an agent based on GPT-5.",
			want: "You are Codex, an agent.",
		},
		{
			in:   "You are Codex, a coding agent based on GPT-5.",
			want: "You are Codex, a coding agent.",
		},
		{
			in:   "You are Codex, an advanced coding agent based on GPT-6.",
			want: "You are Codex, an advanced coding agent.",
		},
	} {
		body := []byte(`{"model":"gemini-3.8-flash-high","instructions":` + mustJSONString(t, tc.in) + `,"input":"hello"}`)
		got, changed, err := rewriteBody(body, "openai-response", "gemini-3.8-flash-high", cfg)
		if err != nil {
			t.Fatalf("rewriteBody() error = %v", err)
		}
		if !changed {
			t.Fatalf("rewriteBody() changed = false for %q", tc.in)
		}
		obj := decodeJSON(t, got)
		if obj["instructions"] != tc.want {
			t.Fatalf("instructions = %q, want %q", obj["instructions"], tc.want)
		}
	}
}

func TestResponsesOnlyRewritesSystemAndDeveloperPromptLocations(t *testing.T) {
	cfg := mustConfig(t, codexRuleYAML)
	old := "You are Codex, a coding agent based on GPT-5."
	want := "You are Codex, a coding agent."
	body := []byte(`{
		"model":"gemini-3.8-flash-high",
		"instructions":"Top: ` + old + `",
		"input":[
			{"role":"developer","content":[{"type":"input_text","text":"Dev: ` + old + `"}]},
			{"role":"system","content":"System: ` + old + `"},
			{"role":"user","content":"User: ` + old + `"},
			{"role":"assistant","content":"Assistant: ` + old + `"},
			{"role":"tool","content":"Tool: ` + old + `"}
		]
	}`)

	got, changed, err := rewriteBody(body, "openai-response", "gemini-3.8-flash-high", cfg)
	if err != nil {
		t.Fatalf("rewriteBody() error = %v", err)
	}
	if !changed {
		t.Fatal("rewriteBody() changed = false")
	}

	text := string(got)
	for _, rewritten := range []string{"Top: " + want, "Dev: " + want, "System: " + want} {
		if !strings.Contains(text, rewritten) {
			t.Fatalf("rewritten body missing %q: %s", rewritten, text)
		}
	}
	for _, preserved := range []string{"User: " + old, "Assistant: " + old, "Tool: " + old} {
		if !strings.Contains(text, preserved) {
			t.Fatalf("rewritten body did not preserve %q: %s", preserved, text)
		}
	}
}

func TestChatAnthropicAndGeminiPromptCoverage(t *testing.T) {
	cfg := mustConfig(t, strings.Replace(codexRuleYAML, "    source_formats:\n      - openai-response\n", "", 1))
	old := "You are Codex, a coding agent based on GPT-5."
	want := "You are Codex, a coding agent."

	tests := []struct {
		name   string
		format string
		body   string
	}{
		{
			name:   "chat",
			format: "openai",
			body:   `{"model":"gemini-3.8-flash-high","messages":[{"role":"system","content":"` + old + `"},{"role":"user","content":"` + old + `"}]}`,
		},
		{
			name:   "anthropic",
			format: "claude",
			body:   `{"model":"gemini-3.8-flash-high","system":[{"type":"text","text":"` + old + `"}],"messages":[{"role":"user","content":"` + old + `"}]}`,
		},
		{
			name:   "gemini",
			format: "gemini",
			body:   `{"model":"gemini-3.8-flash-high","systemInstruction":{"parts":[{"text":"` + old + `"}]},"contents":[{"role":"user","parts":[{"text":"` + old + `"}]}]}`,
		},
		{
			name:   "antigravity-wrapper",
			format: "gemini",
			body:   `{"model":"gemini-3.8-flash-high","request":{"systemInstruction":{"parts":[{"text":"` + old + `"}]},"contents":[{"role":"user","parts":[{"text":"` + old + `"}]}]}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, changed, err := rewriteBody([]byte(tc.body), tc.format, "gemini-3.8-flash-high", cfg)
			if err != nil {
				t.Fatalf("rewriteBody() error = %v", err)
			}
			if !changed {
				t.Fatal("rewriteBody() changed = false")
			}
			if !strings.Contains(string(got), want) {
				t.Fatalf("rewritten body missing %q: %s", want, got)
			}
		})
	}
}

func TestModelAndSourceFormatFilters(t *testing.T) {
	cfg := mustConfig(t, codexRuleYAML)
	body := []byte(`{"instructions":"You are Codex, a coding agent based on GPT-5."}`)

	for _, tc := range []struct {
		format string
		model  string
	}{
		{format: "claude", model: "gemini-3.8-flash-high"},
		{format: "openai-response", model: "claude-sonnet-4-6"},
	} {
		got, changed, err := rewriteBody(body, tc.format, tc.model, cfg)
		if err != nil {
			t.Fatalf("rewriteBody() error = %v", err)
		}
		if changed || got != nil {
			t.Fatalf("unexpected rewrite for format=%q model=%q: %s", tc.format, tc.model, got)
		}
	}
}

func TestInterceptAfterAuthIsAntigravityOnly(t *testing.T) {
	cfg := mustConfig(t, codexRuleYAML)
	configState.Lock()
	configState.cfg = cfg
	configState.Unlock()

	payload := pluginapi.RequestInterceptRequest{
		SourceFormat: "openai-response",
		ToFormat:     "codex",
		Model:        "gemini-3.8-flash-high",
		Body:         []byte(`{"instructions":"You are Codex, a coding agent based on GPT-5."}`),
	}
	raw, _ := json.Marshal(payload)
	result, err := interceptAfterAuth(raw)
	if err != nil {
		t.Fatalf("interceptAfterAuth() error = %v", err)
	}

	var envelope struct {
		Result pluginapi.RequestInterceptResponse `json:"result"`
	}
	if err := json.Unmarshal(result, &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(envelope.Result.Body) != 0 {
		t.Fatalf("non-Antigravity request was modified: %s", envelope.Result.Body)
	}
}

func TestLiteralReplace(t *testing.T) {
	cfg := mustConfig(t, `
rules:
  - name: literal
    mode: replace
    pattern: "foo"
    replacement: "bar"
`)
	body := []byte(`{"instructions":"foo foo","input":"foo"}`)
	got, changed, err := rewriteBody(body, "openai-response", "anything", cfg)
	if err != nil {
		t.Fatalf("rewriteBody() error = %v", err)
	}
	if !changed {
		t.Fatal("rewriteBody() changed = false")
	}
	obj := decodeJSON(t, got)
	if obj["instructions"] != "bar bar" {
		t.Fatalf("instructions = %q", obj["instructions"])
	}
	if obj["input"] != "foo" {
		t.Fatalf("user input changed: %q", obj["input"])
	}
}

func TestInvalidRegexRejected(t *testing.T) {
	_, err := compileConfig([]byte(`
rules:
  - name: broken
    mode: regex_replace
    pattern: "(["
    replacement: "x"
`))
	if err == nil {
		t.Fatal("compileConfig() error = nil, want invalid regex error")
	}
}

func mustJSONString(t *testing.T, value string) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return string(raw)
}
