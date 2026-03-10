# server/conditionals -- Conditional Variable Evaluation

The conditionals package provides parsing and evaluation of conditional variables. A conditional var evaluates a node's trait against an operator to produce a boolean result, used for resource conditional execution and template logic.

## Summary

- Conditional vars are structured YAML maps with a `trait:` field and exactly one operator
- Evaluated server-side during check-in, before resources are sent to the agent
- Result is a `"true"` or `"false"` string, usable in templates and conditional filter fields
- 7 operators: `in`, `not_in`, `gt`, `lt`, `gte`, `lte`, `regex`

## How It Works

A conditional var is defined in a module's `vars:` section as a YAML map instead of a plain string value:

```yaml
vars:
  is_debian:
    trait: os.distro
    in: [debian, ubuntu]
```

At check-in time, this:

1. Looks up `os.distro` in the node's traits using dot-path traversal
2. Applies the `in` operator against `[debian, ubuntu]`
3. Sets `is_debian` to `"true"` or `"false"`

The result can then drive resource inclusion via `when` expressions or template branching:

```yaml
resources:
  - name: apt-setup
    type: exec
    command: apt-get update
    when: "is_debian"
```

## Operators

| Operator | Operand Type | Behavior |
|----------|-------------|----------|
| `in` | list | Trait value (coerced to string) is in the list |
| `not_in` | list | Trait value is NOT in the list |
| `gt` | number | Trait > operand (both coerced to float64) |
| `lt` | number | Trait < operand |
| `gte` | number | Trait >= operand |
| `lte` | number | Trait <= operand |
| `regex` | string | Trait matches the regular expression |

## Examples

### String matching

```yaml
is_ubuntu:
  trait: os.distro
  in: [ubuntu]

not_windows:
  trait: os.family
  not_in: [windows]
```

### Numeric comparison

```yaml
high_memory:
  trait: hardware.memory_mb
  gte: 4096

few_cpus:
  trait: hardware.cpu_count
  lt: 4
```

### Regex matching

```yaml
is_prod_host:
  trait: os.hostname
  regex: ".*\\.prod\\..*"
```

### Nested trait paths

```yaml
is_cloud:
  trait: cloud.provider
  not_in: [none]
```

## Rules

- Exactly one operator per conditional var. Multiple operators produce a parse error.
- The `trait` field is required. A map with operator keys but no `trait` is a validation error.
- Missing traits evaluate to `false` (not an error). This handles nodes that lack certain traits.
- Numeric operators (`gt`, `lt`, `gte`, `lte`) coerce both trait value and operand to float64. Non-numeric values evaluate to `false`.
- `regex` patterns are pre-compiled at parse time. Invalid regex produces a parse error.
- Unknown operator names produce a validation error. Valid operators: `in`, `not_in`, `gt`, `lt`, `gte`, `lte`, `regex`.
- `in` and `not_in` compare string representations (`fmt.Sprintf("%v", val)`).

## Integration with Config System

Conditional vars are separated from regular vars during module loading. They are stored on the `ModuleDefinition` and `NodeConfig` types and evaluated at check-in time when the node's traits are available.

```yaml
name: webserver

vars:
  is_debian:
    trait: os.family
    in: [debian]
  has_enough_ram:
    trait: hardware.memory_mb
    gte: 2048
  port: "80"

resources:
  - name: install-deps
    type: package
    package: libssl-dev
    when: "is_debian"

  - name: configure-cache
    type: file
    targetpath: /etc/app/cache.conf
    content: |
      {{ if eq .Vars.has_enough_ram "true" }}
      cache_size: 1024
      {{ else }}
      cache_size: 256
      {{ end }}
```

### Evaluation order

1. Module and node vars are merged (regular string vars only)
2. Conditional vars are evaluated against the node's traits
3. Conditional results are injected into the vars map as `"true"` / `"false"` strings
4. Resources with `when` expressions are evaluated at execution time on the agent
5. Templates are rendered with the final vars (including conditional results)

### Precedence

Conditional vars take precedence over node-level vars of the same name. If a module defines `is_debian` as a conditional var and a node entry also sets `is_debian: "true"` as a plain var, the conditional evaluation wins.

## API

The package exports three main functions:

- `IsConditionalVar(v any) bool` -- returns true if a YAML value is a map with a `trait` key
- `ParseConditionalVar(name string, raw map[string]any) (ConditionalVar, error)` -- validates and parses a raw YAML map
- `Evaluate(cv ConditionalVar, traits map[string]any) bool` -- resolves the trait and applies the operator
