# CPA Antigravity Request Rewrite

A CLIProxyAPI v7 request interceptor for safely rewriting system/developer prompt text **only when the selected upstream is Antigravity**.

The plugin is designed for compatibility fixes that should not be hard-coded into CLIProxyAPI itself. Rules are configured in YAML and can use either literal replacement or precompiled Go regular expressions.

## Why

Some Antigravity upstream requests are sensitive to client-injected identity text. A verified example is the Codex identity family:

```text
You are Codex, a coding agent based on GPT-5.
```

Instead of hard-coding one exact sentence, the plugin can normalize a family of variants using a configurable regex.

## Safety model

- Runs at `request.intercept_after`.
- Does nothing unless `ToFormat == "antigravity"`.
- Rewrites only known system/developer prompt locations.
- Does not recursively scan arbitrary JSON.
- Leaves user/assistant/tool content, tool definitions/results, function arguments and reasoning untouched.
- Invalid or unsupported request JSON fails open and is forwarded unchanged.
- Regexes are compiled when plugin configuration is loaded, not on every request.

## Supported prompt locations

| Protocol shape | Rewritten locations |
| --- | --- |
| OpenAI Responses | top-level `instructions`; `input[]` messages with role `system` or `developer` |
| OpenAI Chat Completions | `messages[]` with role `system` or `developer` |
| Anthropic Messages | top-level `system` string / text blocks |
| Gemini | `systemInstruction.parts[].text`, `system_instruction.parts[].text` |
| Antigravity wrapper | `request.systemInstruction.parts[].text`, `request.system_instruction.parts[].text` |

## Configuration

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    antigravity-request-rewrite:
      enabled: true
      priority: 1
      rules:
        - name: codex-identity-normalize
          mode: regex_replace
          source_formats:
            - openai-response
          scopes:
            - system
            - developer
          models:
            - "gemini-*"
          pattern: '(?i)(You are Codex,\s+[^.]+?)\s+based on\s+[^.]+(\.?)'
          replacement: '${1}${2}'
```

This turns examples such as:

```text
You are Codex, an agent based on GPT-5.
You are Codex, a coding agent based on GPT-5.
You are Codex, an advanced coding agent based on GPT-6.
```

into:

```text
You are Codex, an agent.
You are Codex, a coding agent.
You are Codex, an advanced coding agent.
```

### Rule fields

- `name`: optional human-readable rule name.
- `enabled`: optional boolean, defaults to true.
- `mode`: `replace` or `regex_replace`.
- `pattern`: literal source text or Go RE2-compatible regular expression.
- `replacement`: replacement text. Regex capture groups use Go replacement syntax such as `${1}`.
- `source_formats`: optional list. Common values are `openai-response`, `openai`, `claude`, and `gemini`. Empty means any source format.
- `scopes`: optional list containing `system` and/or `developer`. Empty defaults to both.
- `models`: optional wildcard list. `*` matches any sequence and `?` matches one character. Matching is case-insensitive.

Rules are applied in order.

## Build

CLIProxyAPI dynamic plugins use CGO.

```bash
CGO_ENABLED=1 go build -buildmode=c-shared -o antigravity-request-rewrite.so .
```

For the Oracle A1 / Linux ARM64 target, use the GitHub Actions artifact produced by the repository workflow.

## Verify

After installing the shared library and restarting CLIProxyAPI:

```text
GET /v0/management/plugins
```

Confirm `antigravity-request-rewrite` reports `registered: true` and `effective_enabled: true`.

Then send an OpenAI Responses request through an Antigravity-backed model and verify the configured identity text is normalized before upstream execution.

## Development

```bash
go test ./...
go vet ./...
```

## License

MIT
