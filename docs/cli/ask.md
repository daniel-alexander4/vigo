# hobnobcli ask

Ask the AI assistant a natural language question about your fleet. The assistant can query thralls, runs, traits, compliance, configs, modules, and packages to answer questions.

## Usage

```bash
hobnobcli ask [question] [flags]
```

If no question argument is given, reads from stdin. This is useful for piping configuration files for review.

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--thrall` | | | Thrall hostname context for the question |

## Examples

### Ask a fleet-wide question

```bash
hobnobcli ask "how many thralls do I have?"
```

```
You currently have 12 enrolled thralls, all active. 10 are compliant,
1 is drifted, and 1 is failing.

[tools: list_thralls, compliance_summary | tokens: 245]
```

### Ask about a specific thrall

```bash
hobnobcli ask --thrall web-03 "why is it failing?"
```

```
web-03.example.com is failing because the nginx package resource
returned an error during the last run: "apt-get install failed:
E: Unable to locate package nginx-extras". The module specifies
package "nginx-extras" but the available package on Ubuntu 24.04
is "nginx-extra" (without the 's').

[tools: thrall_detail, recent_runs | tokens: 412]
```

### Ask for module generation

```bash
hobnobcli ask "write a module for nginx with TLS"
```

### Pipe a config file for review

```bash
hobnobcli ask < policy/proposed/modules/nginx.hnb
```

The piped content is automatically prefixed with "Review this configuration:" before being sent to the AI.

## Streaming

The response is streamed via Server-Sent Events (SSE) when the server supports it. Tool usage status updates are shown on stderr while the answer streams to stdout.

## Related

- [thralls](thralls.md) -- manage enrolled thralls
- [query](query.md) -- query trait values directly
- [validate](validate.md) -- validate .hnb files programmatically
