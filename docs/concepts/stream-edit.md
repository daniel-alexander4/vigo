
# Stream-Edit

The `file` resource accepts a `stream_edit:` attribute that pipes the file's rendered content through one or more agent-local scripts (stdin → stdout) before it's written to disk. This is the mechanism for turning a template into a lightly-transformed artifact without authoring a full executor or a second resource.

```yaml
resources:
  - name: "hardened sshd config"
    type: file
    path: /etc/ssh/sshd_config
    source: templates/sshd_config.tmpl
    stream_edit:
      - /srv/vigo/scripts/redact_comments.sh
      - /srv/vigo/scripts/normalize_whitespace.py
```

Each script in the list receives the previous stage's output on stdin and writes the transformed content to stdout. The final script's output is what gets written to the target path. Results are cached by (content hash × script path × script mtime), so unchanged inputs don't re-run the pipeline every convergence cycle.

## Why it exists

Two common needs motivate this attribute:

1. **Post-process a template output.** The built-in Go template engine renders `.Vars` and `.Traits` into the file, but sometimes you need to reformat, redact, or re-key values based on logic the template language can't cleanly express (e.g. sort a list, strip comments, run through a linter/formatter).
2. **Borrow a widely-understood formatter.** `jq`, `yq`, `sed`, `awk`, formatters like `black` or `rustfmt` — all of them read stdin and write stdout, so they work out of the box as stream-edit stages.

Use it when a transformation is cheap, deterministic, and genuinely helps; avoid it when the equivalent logic would live more clearly in the template or a dedicated resource.

## Safety model

Scripts run as the agent user (root, on most envoys) and can do anything that user can. Vigo guardrails the feature so it doesn't become an unexamined backdoor:

### `stream_edit.enabled`

Master switch. Default is `true` — stream-edit is available. Set to `false` in hardened deployments where operators want to guarantee no agent-local script execution through this path.

```yaml
stream_edit:
  enabled: false
```

Modules that reference `stream_edit:` against a disabled server receive a resource-level error at check-in and the file won't be written.

### `stream_edit.allowed_paths`

Directories that stream-edit scripts must reside within. Scripts outside the allowlist are rejected. Default is `[/srv/vigo/scripts]`.

```yaml
stream_edit:
  allowed_paths:
    - /srv/vigo/scripts
    - /opt/vigo/bin
```

Tighten this if you want a specific pinned-down script directory managed separately from stockpile content.

### `stream_edit.default_timeout`

Per-script wall-clock timeout as a Go duration string. Default is `10s`. Scripts that exceed the timeout are killed and the stage reports an error. Set higher only for genuinely-slow transforms (a multi-megabyte yq roll-up on a slow host); lower is fine for mostly-instant scripts.

```yaml
stream_edit:
  default_timeout: "30s"
```

## Caching

Stream-edit results are cached across convergence cycles keyed on:

- The input content hash (SHA-256 of stdin)
- The script's absolute path
- The script's mtime

A cache hit skips execution and reuses the cached output; a cache miss runs the pipeline and stores the result. Because mtime is part of the key, rotating a script via `chmod`/`mv` or a Vigo-managed deployment invalidates the cache automatically.

Cache entries live under the agent's state directory and are pruned when their content becomes unreferenced. Restarting the agent does not clear the cache.

## Errors and idempotency

A script that exits non-zero fails the whole pipeline; the resource is reported as failed and the file is not written. The next convergence attempt re-runs the pipeline from scratch (cache miss is forced on the stage that failed).

Scripts should be pure transformations — stateless, deterministic, no side effects. Writing to disk, hitting the network, or depending on external state defeats caching and creates flaky convergence.

## Related

- [Server-config > stream_edit](../getting-started/server-config.md#stream_edit) — config reference
- [Resource Language](resource-language.md) — the `file` resource and its attributes
- [Templates](templates.md) — upstream rendering before stream-edit stages
- [Guardrails](guardrails.md) — the broader self-protection model for agent-side execution
