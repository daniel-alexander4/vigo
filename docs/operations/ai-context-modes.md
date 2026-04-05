
# Context Modes

The AI assistant supports three context modes that control how fleet data is provided to the LLM. Configure with `ai.context_mode` in `server.yaml`.

## tools (Default for Claude, OpenAI)

The LLM receives tool definitions and calls them iteratively to gather data.

```yaml
ai:
  context_mode: tools
```

**How it works:**
1. The system prompt describes the Vigo fleet and available tools
2. The LLM decides which tools to call based on the question
3. Tool results are fed back to the LLM
4. The loop repeats until the LLM produces a final answer (up to `max_tool_rounds`)

**Best for:** Providers with reliable tool-calling support (Claude, GPT-4o).

## prefetch

Data is pre-fetched based on keyword analysis of the question and injected into the prompt. No tools are sent to the LLM.

```yaml
ai:
  context_mode: prefetch
```

**How it works:**
1. The UI sends structured page context (envoy hostname, run ID, compliance framework, page name)
2. The question is also scanned for keywords (hostnames, "compliance", "module", "package", etc.)
3. Relevant data is fetched proactively based on both page context and keywords:
   - **Envoy pages** (detail, trace, inventory): envoy details, traits, recent runs, config
   - **Run detail page**: specific run results + envoy context
   - **Compliance pages**: framework-specific compliance data
   - **Dashboard/fleet/envoys pages**: full envoy list
   - **Runs page**: recent runs across all envoys
   - **Risk page**: fleet data for risk context
   - **Keyword fallback** (when no page context): hostname extraction, compliance/module/role/package keywords
4. All fetched data is appended to the question as "Prefetched Data"
5. The LLM answers using only the prefetched context (no tool calls)

**Best for:** Providers with weak or unreliable tool-calling support, or when you want faster responses at the cost of flexibility.

### Page-Aware Context

The web UI automatically detects which page the user is on and passes structured context to the assistant via `data-ai-*` HTML attributes. This eliminates the need for hostname guessing from question text. Pages that provide context:

| Page | Context Sent |
|------|-------------|
| Envoy detail | `envoy` hostname |
| Run detail | `envoy` hostname + `run_id` |
| Compliance report | `framework` slug |
| Envoy compliance | `envoy` hostname + `framework` slug |
| Config trace | `envoy` hostname |
| Inventory detail | `envoy` hostname |
| Dashboard, Envoys, Fleet | `page` name (triggers fleet prefetch) |
| Compliance overview | `page` name (triggers compliance prefetch) |
| Risk, Runs | `page` name (triggers relevant prefetch) |

### Hostname Detection (Fallback)

When no page context is available (e.g., API calls without context), the prefetcher identifies hostnames by looking for words containing dots or hyphens that are longer than 3 characters and are not common English contractions (don't, can't, e.g., etc.).

## auto (Default for Ollama)

Tries tools first, falls back to prefetch if the LLM produces malformed tool calls.

```yaml
ai:
  context_mode: auto
```

**How it works:**
1. Tools are sent to the LLM
2. If the first round produces empty/malformed tool call names, the assistant detects this and:

   - Removes tools from subsequent rounds
   - Pre-fetches data and appends it to the context
3. The LLM then answers using prefetched context

**Best for:** Ollama and other local models where tool-calling reliability varies by model.

## docs_mode

Controls whether Vigo documentation is injected into the system prompt, giving the LLM knowledge about Vigo concepts and configuration.

```yaml
ai:
  docs_mode: full        # inject full documentation (default)
  docs_mode: condensed   # inject a condensed summary
  docs_mode: "off"       # no documentation injection
```

| Mode | Description |
|------|-------------|
| `full` | Loads the complete docs corpus from the docs directory and includes it in the system prompt. Provides the richest context but uses more tokens. |
| `condensed` | Loads the docs and applies condensation (removes examples, shortens descriptions). Balanced between context quality and token usage. |
| `off` | No documentation is injected. The LLM relies entirely on its training data and tool results. Saves tokens but may produce less accurate answers about Vigo-specific concepts. |

The docs directory path is configured at server startup. When available, the corpus is loaded once and cached in the assistant's system prompt.

## Choosing a Mode

| Scenario | Recommended Mode |
|----------|-----------------|
| Claude or GPT-4o with reliable tools | `tools` |
| Ollama with qwen2.5 or similar | `auto` |
| Fast responses, simple questions | `prefetch` |
| Small context window model | `prefetch` + `docs_mode: off` |
| Large context window model | `tools` + `docs_mode: full` |
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
