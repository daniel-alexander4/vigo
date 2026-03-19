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
1. The question is scanned for keywords (hostnames, "compliance", "module", "package", etc.)
2. Relevant data is fetched proactively:
   - If a hostname is detected: envoy details, traits, recent runs, config
   - If compliance-related words: fleet compliance summary
   - If module/role words: module and role lists
   - If envoy/fleet words: envoy list
   - If package/drift words: package drift data
3. All fetched data is appended to the question as "Prefetched Data"
4. The LLM answers using only the prefetched context (no tool calls)

**Best for:** Providers with weak or unreliable tool-calling support, or when you want faster responses at the cost of flexibility.

### Hostname Detection

The prefetcher identifies hostnames by looking for words containing dots or hyphens that are longer than 3 characters and are not common English contractions (don't, can't, e.g., etc.).

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
