
# vigocli traits

Search and explore the trait dot-paths available for use in templates (`.Traits.<key>`) and `when:` expressions. Useful when writing configcrates and you want to know what facts the agent exposes.

## Subcommands

| Verb | Purpose |
|---|---|
| `search <keyword>` | Search trait keys by keyword (matches against dot-path segments) |

Run `vigocli traits --help` for current flags.

## Related

- [Trait Collectors](../vigo/traits/_index.md) — per-collector schema reference
- [When Expressions](../../reference/configcrate-language.md) — how traits flow into condition evaluation
- [Templates](../../reference/configcrate-language.md) — how traits flow into `content:` rendering

## Note on `vigo traits`

The agent binary has its own `vigo traits` subcommand that actually runs the collectors locally and prints the results as JSON — see [Agent CLI](agent.md) for that. The `vigocli traits` command here is an admin-side *schema browser* that looks up what names exist; the agent-side command is the one that produces values for a specific host.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../legal/license.md).*
