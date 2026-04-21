# Tag/Host/Trait Lookup Tables

Lookup tables let a single resource definition vary its field values per envoy without duplicating the whole resource. They are the primary way to express "for hosts tagged X, this field is Y" in vigo configs.

## Motivation

A user named `dan` needs to exist on every envoy, but:

- On `danlap` (a workstation) he wants to be in `virtualbox` and `kvm` groups and use `/bin/fish`.
- On `plex.home` (a media server) he wants to be in the `plex` group and use `/bin/bash`.
- On every other machine he just wants to be in `docker` with `/bin/bash`.

Writing three separate resources — one per host — would work but scales poorly. A lookup table expresses the variation inline:

```yaml
resources:
  - name: dan
    type: user
    username: dan
    groups:
      tag:workstation:  [docker, virtualbox, kvm]
      tag:mediaserver:  [docker, plex]
      default:          [docker]
    shell:
      danlap:  /bin/fish
      default: /bin/bash
    sudo_nopasswd:
      tag:workstation: true
      default:         false
```

One resource, three per-envoy-varying fields. Every other field (`name`, `username`, …) stays literal.

## Syntax

A lookup table is a YAML map assigned as the value of a resource attribute. The map **must** contain a `default` key — that key is how the resolver tells a lookup table apart from a literal map value.

```yaml
<field>:
  <spec>: <value>
  <spec>: <value>
  default: <value>
```

Each `<spec>` uses the same mini-language as `vigocli ... --target`:

| Spec form        | Example             | Matches |
|------------------|---------------------|---------|
| Exact hostname   | `danlap`            | Envoy whose hostname equals `danlap` |
| Hostname glob    | `*.web.prod`        | Envoys whose hostname matches the glob (`*`/`?`) |
| Tag              | `tag:workstation`   | Envoys tagged `workstation` in their match block |
| Tag glob         | `tag:*web*`         | Envoys whose tags match the glob |
| Trait filter     | `os.distro=ubuntu`  | Envoys whose trait `os.distro` equals `ubuntu` |
| `default`        | `default`           | Fallback when no other arm matches |

Tags come from the `tags:` field on the envoy's match block in `envoys.vgo`. Traits come from the agent's latest reported trait snapshot. Hostname is the envoy's actual hostname, not its match pattern.

## Precedence

Lookup resolution depends on the value type:

### List-valued lookups: union all matching arms

If every arm (including `default`) is a YAML list, the resolver unions all matching arms in declaration order and deduplicates repeated scalar elements.

```yaml
groups:
  tag:workstation: [docker, virtualbox]
  tag:mediaserver: [docker, plex]
  danlap:          [kvm]
  default:         []
```

For `danlap` (which is tagged `workstation`), the result is `[docker, virtualbox, kvm]` — union of the `tag:workstation` arm and the `danlap` arm. Declaration order determines output order; duplicates (like `docker`) only appear once.

### Scalar-valued lookups: most-specific match wins

If every arm is a scalar (string, bool, int), the resolver picks the most specific match. Specificity ranking, highest first:

1. Exact hostname (e.g., `danlap`)
2. Trait filter (e.g., `os.distro=ubuntu`)
3. Tag (e.g., `tag:workstation`)
4. Hostname glob (e.g., `*.prod.example.com`)

Ties within a rank are broken by declaration order. This means **you can put the arms in any order** — the resolver picks the right one. Your `danlap` host-specific override will always win over `tag:workstation` even if it appears later in the file.

```yaml
shell:
  tag:workstation: /bin/zsh       # wins for workstations without a more specific arm
  danlap:          /bin/fish      # wins for danlap regardless of file position
  default:         /bin/bash      # wins for everyone else
```

### Mixed-type arms are a config error

A lookup table cannot mix list and scalar values across its arms. This catches `groups: { tag:foo: [a, b], default: "bad" }` at config load time.

## `default` is mandatory

Every lookup table must have a `default` arm. This is the discriminator that tells the resolver "this map is a lookup table" (rather than a literal map value), and it also forces the author to think about the no-match case explicitly. If no arm matches and no default is present, it's a config bug.

Use `default: []` for list fields that should be empty when no arm matches, `default: null` for optional scalars, or write a sensible fallback value.

## What's NOT supported (yet)

- **Lookup tables in `vars:` blocks.** Only resource attributes are inspected today. If you want a lookup table that feeds multiple resources, duplicate it at each resource — or ask for vars-level support.
- **Nested lookups.** Only top-level resource attributes are resolved. `{ foo: { bar: { tag:x: y, default: z } } }` doesn't work.
- **Literal maps with a `default` key.** If you want a map-typed resource attribute whose keys include the string `default`, you'll need to escape or rename that key. Vanishingly rare in practice.

## How it works under the hood

At config load time, vigo scans every resource attribute and sets `HasLookups=true` on the `EnvoyConfig` if any attribute is a lookup table. At check-in time, configs with `HasLookups=true` bypass the pattern-level policy cache and get a freshly resolved bundle per envoy — the same mechanism that `case:` conditional resources already use. The cost is a bundle rebuild per check-in for affected envoys; for configs without lookups, nothing changes.
