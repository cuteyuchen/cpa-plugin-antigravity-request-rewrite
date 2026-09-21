# CPA Request Rewrite

A CLIProxyAPI v7 request interceptor for safely rewriting system/developer prompt text on explicitly selected upstream formats. It can target Antigravity, Codex, xAI/Grok, or any other CPA `ToFormat` value.

The plugin is designed for compatibility fixes that should not be hard-coded into CLIProxyAPI itself. Rules are configured in YAML and can use either literal replacement or precompiled Go regular expressions.

## Why

Some upstream compatibility cases require normalizing client-injected identity text before provider-specific request execution. One tested example is the Codex identity family:

```text
You are Codex, a coding agent based on GPT-5.
```

Instead of hard-coding one exact sentence, the plugin can normalize a family of variants using a configurable regex.

## Safety model

- Runs at `request.intercept_after`.
- Runs only when `ToFormat` matches `target_formats`. If `target_formats` is omitted, it defaults to `antigravity`.
- Accepts `grok`, `x-ai`, and `x.ai` as aliases for CPA's canonical `xai` target format.
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
    request-rewrite:
      enabled: true
      priority: 1

      target_formats:
        - antigravity
        - codex
        - grok

      rules:
        - name: codex-identity-normalize
          mode: regex_replace
          source_formats:
            - openai-response
          scopes:
            - system
            - developer
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

### Target formats

`target_formats` controls which selected upstream formats are allowed to run the rewrite rules. It is matched against CPA's after-auth `req.ToFormat`, not the model name.

Common values include:

- `antigravity`
- `codex`
- `xai` (you may also write `grok`, `x-ai`, or `x.ai`)
- `gemini`
- `claude`
- `openai`
- `kimi`
- `*` to allow any target format

If the field is omitted or empty, the plugin defaults to `antigravity`, preserving the original behavior.

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

## Supported platforms

Release assets cover every platform/architecture for which the official CLIProxyAPI release currently enables dynamic-library plugins:

| OS | Architecture | Asset |
| --- | --- | --- |
| Linux (glibc) | amd64 | `request-rewrite_<version>_linux_amd64.zip` |
| Linux (glibc) | arm64 | `request-rewrite_<version>_linux_arm64.zip` |
| macOS (Darwin) | amd64 | `request-rewrite_<version>_darwin_amd64.zip` |
| macOS (Darwin) | arm64 | `request-rewrite_<version>_darwin_arm64.zip` |
| Windows | amd64 | `request-rewrite_<version>_windows_amd64.zip` |
| Windows | arm64 | `request-rewrite_<version>_windows_arm64.zip` |
| FreeBSD | amd64 | `request-rewrite_<version>_freebsd_amd64.zip` |

Linux release libraries are built against the same GLIBC 2.17 baseline used by the official plugin-capable CLIProxyAPI Linux builds.

CLIProxyAPI also publishes `*_no-plugin` binaries for some platforms, including portable Linux builds and FreeBSD arm64. Those binaries intentionally disable dynamic-library plugins, so this project does not publish plugin assets for those targets.

## Build

CLIProxyAPI dynamic plugins use CGO.

```bash
CGO_ENABLED=1 go build -buildmode=c-shared -o request-rewrite.so .
```

For installed releases, download the ZIP matching the CPA host OS and architecture from GitHub Releases and verify it against `checksums.txt`.

## Verify

After installing the shared library and restarting CLIProxyAPI:

```text
GET /v0/management/plugins
```

Confirm `request-rewrite` reports `registered: true` and `effective_enabled: true`.

Then send an OpenAI Responses request through one of the configured target upstreams and verify the configured identity text is normalized before upstream execution.

## Development

```bash
go test ./...
go vet ./...
```

## License

MIT
