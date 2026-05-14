
# vigocli ask

Ask the AI assistant a natural language question about your fleet. The assistant can query envoys, runs, traits, compliance, configs, modules, and packages to answer questions.

## Usage

```bash
vigocli ask [question] [flags]
```

If no question argument is given, reads from stdin. This is useful for piping configuration files for review.

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--envoy` | | | Envoy hostname context for the question |

## Examples

### Ask a fleet-wide question

```bash
vigocli ask "how many envoys do I have?"
```

```
You currently have 12 enrolled envoys, all active. 10 are compliant,
1 is relapsed, and 1 has errors.

[tools: list_envoys, compliance_summary | tokens: 245]
```

### Ask about a specific envoy

```bash
vigocli ask --envoy web-03 "why is it failing?"
```

```
web-03.example.com is failing because the nginx package resource
returned an error during the last run: "apt-get install failed:
E: Unable to locate package nginx-extras". The module specifies
package "nginx-extras" but the available package on Ubuntu 24.04
is "nginx-extra" (without the 's').

[tools: envoy_detail, recent_runs | tokens: 412]
```

### Ask for module generation

```bash
vigocli ask "write a module for nginx with TLS"
```

### Pipe a config file for review

```bash
vigocli ask < stockpile/modules/nginx.vgo
```

The piped content is automatically prefixed with "Review this configuration:" before being sent to the AI.

## Streaming

The response is streamed via Server-Sent Events (SSE) when the server supports it. Tool usage status updates are shown on stderr while the answer streams to stdout.

## Related

- [envoys](envoys.md) -- manage enrolled envoys
- [query](query.md) -- query trait values directly
- [validate](validate.md) -- validate .vgo files programmatically
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/license.md).*
