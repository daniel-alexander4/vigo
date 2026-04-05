
# AI Providers

The Vigo AI assistant supports four LLM providers. Configure the provider in the `ai:` section of `server.yaml`.

For the best experience, use Claude or OpenAI. Local models work for air-gapped environments but produce noticeably lower-quality answers, especially for multi-step tool use.

## Claude (Recommended)

Anthropic's Claude API. Best tool-calling accuracy and reasoning quality.

```yaml
ai:
  enabled: true
  provider: claude
  model: claude-sonnet-4-20250514
  api_key: "secret:vigo/ai/claude_api_key"
```

| Field | Default | Description |
|-------|---------|-------------|
| `model` | `claude-sonnet-4-20250514` | Claude model name |
| `api_key` | (required) | Anthropic API key (supports `secret:` prefix) |

## OpenAI

OpenAI's GPT API.

```yaml
ai:
  enabled: true
  provider: openai
  model: gpt-4o
  api_key: "secret:vigo/ai/openai_api_key"
```

| Field | Default | Description |
|-------|---------|-------------|
| `model` | `gpt-4o` | OpenAI model name |
| `api_key` | (required) | OpenAI API key (supports `secret:` prefix) |

## Ollama

Free, local, self-hosted LLM inference via [Ollama](https://ollama.com). Useful for air-gapped or offline environments where external API calls are not permitted. Ollama is the default provider because it requires no API keys or external accounts.

**Be realistic about hardware.** Ollama runs the model locally, and a 7B-parameter model on CPU is slow (60+ seconds for cold-load, several seconds per response). For usable performance, run Ollama on a machine with a dedicated GPU (NVIDIA with 8+ GB VRAM, or Apple Silicon with sufficient unified memory). CPU-only inference works but the experience will be poor.

**Quality tradeoff.** Local 7B models produce significantly lower-quality answers than Claude or GPT-4o, particularly for multi-step reasoning and tool use. The assistant has 14 fleet tools it can call in sequence — small models frequently make incorrect tool calls or misinterpret results. The context mode defaults to `auto` for Ollama because some models have inconsistent tool-calling support; auto mode tries tools first and falls back to prefetch if tool calls are malformed.

```yaml
ai:
  enabled: true
  provider: ollama
  model: qwen2.5:7b
  base_url: http://ollama:11434/v1
```

| Field | Default | Description |
|-------|---------|-------------|
| `model` | `qwen2.5:7b` | Any Ollama-hosted model with tool-calling support |
| `base_url` | `http://ollama:11434/v1` | Ollama OpenAI-compatible endpoint |

Vigo automatically pulls the configured model on startup if it is not already downloaded, and warms it into memory to avoid cold-load delays on the first request.

## OpenAI-Compatible

Any provider that implements the OpenAI chat completions API — including self-hosted inference servers like [vLLM](https://docs.vllm.ai/), [llama.cpp](https://github.com/ggerganov/llama.cpp), or [text-generation-inference](https://github.com/huggingface/text-generation-inference). This is the right choice if you want local inference without Ollama, or if you run a shared GPU inference cluster.

```yaml
ai:
  enabled: true
  provider: openai-compat
  model: mixtral-8x7b
  base_url: https://my-inference-server.internal/v1
  api_key: "secret:vigo/ai/api_key"
```

| Field | Default | Description |
|-------|---------|-------------|
| `model` | (required) | Model name for the provider |
| `base_url` | (required) | OpenAI-compatible API base URL |
| `api_key` | | API key if required by the provider |

Example with a local vLLM server:

```yaml
ai:
  enabled: true
  provider: openai-compat
  model: Qwen/Qwen2.5-7B-Instruct
  base_url: http://gpu-server.internal:8000/v1
```

The same GPU recommendations apply as with Ollama — a dedicated GPU is required for acceptable latency.

## Data Privacy

The Vigo AI assistant sends fleet data (envoy names, traits, convergence results, module definitions) to the configured LLM provider as context for answering questions. If you use an external provider (Claude, OpenAI, or a third-party OpenAI-compatible service), verify that your agreement with that provider prohibits using your data for model training. Both Anthropic and OpenAI offer API terms that exclude training on API inputs by default, but you should confirm this for your specific plan or agreement. If your data cannot leave your network, use Ollama or openai-compat pointed at a self-hosted inference server.

## Common Configuration

These settings apply to all providers:

```yaml
ai:
  enabled: true
  provider: claude
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
| `context_mode` | Varies by provider | How context is provided (see [Context Modes](ai-context-modes.md)) |
| `docs_mode` | `full` | Documentation injection mode (see [Context Modes](ai-context-modes.md)) |

## Rate Limiting

When `rate_limit` is set, the assistant enforces a per-minute token bucket. Requests that exceed the limit receive a `429`-equivalent error:

```
"rate limit exceeded (10 requests/minute)"
```

The rate limiter resets every minute.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
