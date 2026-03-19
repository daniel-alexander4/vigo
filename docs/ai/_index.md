# AI Assistant

Vigo includes a built-in AI assistant accessible via the `/ask` endpoint (REST API) and the web UI at `/ask`. The assistant answers questions about your fleet by querying envoy state, traits, compliance, runs, and configuration through a tool-use loop.

## Key Properties

- **Read-only**: The assistant can only read data. It cannot modify envoys, trigger tasks, change configuration, or perform any write operations.
- **Tool-use loop**: The assistant has access to 14 tools that query the Vigo server. It calls tools iteratively to gather data, then synthesizes an answer.
- **Streaming responses**: The UI uses server-sent events (SSE) for real-time response streaming, showing which tools are being called.
- **Secret redaction**: All tool results pass through a secret stripping filter before being included in the LLM context. No `secret:` values are exposed.

## Usage

### Web UI

Navigate to `/ask` in the Vigo web UI. Type a question and the assistant will respond with streaming output, showing tool calls in real time.

### REST API

```bash
# Synchronous response
curl -X POST https://server:8443/api/v1/ask \
  -H 'Content-Type: application/json' \
  -d '{"question": "Why is web-01.example.com failing?"}'

# Streaming response
curl -X POST https://server:8443/api/v1/ask \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  -d '{"question": "Show me the fleet compliance status"}'
```

### CLI

```bash
vigogo ask "What packages are drifted across the fleet?"
```

## Multi-Turn Conversations

The API supports conversation history for follow-up questions:

```json
{
  "question": "What about its traits?",
  "context": {"envoy": "web-01.example.com"},
  "history": [
    {"role": "user", "content": "Tell me about web-01"},
    {"role": "assistant", "content": "web-01.example.com is a..."}
  ]
}
```

## Further Reading

- [AI Providers](providers.md) -- Ollama, Claude, OpenAI, OpenAI-compat
- [LLM Tools](tools.md) -- all 14 tools available to the assistant
- [Context Modes](context-modes.md) -- tools, prefetch, auto
