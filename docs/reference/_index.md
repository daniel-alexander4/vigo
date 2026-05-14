
# Reference

Lookup material. Not a tutorial — get the shape of what you need from the [how-to guides](../howto/) first if you don't already know it.

| Section | What's there |
|---|---|
| [vigocli/](vigocli/) | Every CLI command, one page per verb. |
| [vigo/](vigo/) | The agent: flags, plus the full resource and trait vocabulary that you write modules in. |
| [API](api.md) | REST API endpoints. The audit test (`go test ./test/audit/...`) verifies every `/api/v1/*` route in the codebase appears here. |
| [server.yaml](server-yaml.md) | The full server-config schema. |
| [Writing modules](writing-modules.md) | The canonical module-authoring reference: YAML format, resource types, conditionals, templates, composition (roles / matchfiles / usercrates / common.vgo / environments.vgo), lookups, retraction, stream-edit. |
| [Compliance matrix](compliance-matrix.md) | Framework × control table; which Vigo modules / Connwaer traits satisfy each control. |

---
*Confidential — Alexander4, LLC. Not for redistribution. See [../legal/license.md](../legal/license.md).*
