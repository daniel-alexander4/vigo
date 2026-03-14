# AI Providers

The Hobnob AI assistant supports four LLM providers. Configure the provider in the `ai:` section of `server.yaml`.

## Ollama (Default)

Free, local, self-hosted LLM inference. Recommended for air-gapped or cost-sensitive deployments.

```yaml
ai:
  enabled: true
  provider: ollama
  model: qwen2.5:7b
  base_url: http://ollama:11434/v1
```

| Field | Default | Description |
|-------|---------|-------------|
| `model` | `qwen2.5:7b` | Any Ollama-hosted model |
| `base_url` | `http://ollama:11434/v1` | Ollama OpenAI-compatible endpoint |

Ollama uses the OpenAI-compatible API internally. The `qwen2.5:7b` model requires approximately 4.4 GB of RAM and takes about 60 seconds for cold-load on CPU.

**Note**: The context mode defaults to `auto` for Ollama because some models have inconsistent tool-calling support. Auto mode tries tools first and falls back to prefetch if tool calls are malformed.

## Claude

Anthropic's Claude API. Requires an API key.

```yaml
ai:
  enabled: true
  provider: claude
  model: claude-sonnet-4-20250514
  api_key: "secret:hobnob/ai/claude_api_key"
```

| Field | Default | Description |
|-------|---------|-------------|
| `model` | `claude-sonnet-4-20250514` | Claude model name |
| `api_key` | (required) | Anthropic API key (supports `secret:` prefix) |

## OpenAI

OpenAI's GPT API. Requires an API key.

```yaml
ai:
  enabled: true
  provider: openai
  model: gpt-4o
  api_key: "secret:hobnob/ai/openai_api_key"
```

| Field | Default | Description |
|-------|---------|-------------|
| `model` | `gpt-4o` | OpenAI model name |
| `api_key` | (required) | OpenAI API key (supports `secret:` prefix) |

## OpenAI-Compatible

Any provider that implements the OpenAI chat completions API. Requires `base_url`.

```yaml
ai:
  enabled: true
  provider: openai-compat
  model: mixtral-8x7b
  base_url: https://my-inference-server.internal/v1
  api_key: "secret:hobnob/ai/api_key"
```

| Field | Default | Description |
|-------|---------|-------------|
| `model` | (required) | Model name for the provider |
| `base_url` | (required) | OpenAI-compatible API base URL |
| `api_key` | | API key if required by the provider |

## Common Configuration

These settings apply to all providers:

```yaml
ai:
  enabled: true
  provider: ollama
  max_tokens: 4096
  rate_limit: 10
  max_tool_rounds: 10
  context_mode: auto
  docs_mode: full
```

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | `false` | Enable the AI assistant |
| `max_tokens` | `4096` | Maximum tokens in the LLM response |
| `rate_limit` | `0` (unlimited) | Maximum requests per minute |
| `max_tool_rounds` | `10` | Maximum tool-call iterations before returning |
| `context_mode` | Varies by provider | How context is provided (see [Context Modes](context-modes.md)) |
| `docs_mode` | `full` | Documentation injection mode (see [Context Modes](context-modes.md)) |

## Rate Limiting

When `rate_limit` is set, the assistant enforces a per-minute token bucket. Requests that exceed the limit receive a `429`-equivalent error:

```
"rate limit exceeded (10 requests/minute)"
```

The rate limiter resets every minute.
