# vigogo test

Run config tests that validate resolved policy against expected outcomes. Tests verify that the config tree (modules, roles, envoys) produces the expected resources, vars, and module assignments for a given hostname — without connecting to the server.

Equivalent to rspec-puppet or ChefSpec: test the config compilation, not the execution.

## Usage

```bash
# Run all tests in the default tests/ directory
vigogo test

# Run a single test file
vigogo test tests/nginx-module.test.vgo

# Run all tests in a directory
vigogo test tests/

# Specify a different config directory
vigogo test --config-dir /srv/vigo/policy/stage
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--config-dir` | `/srv/vigo/policy/stage` | Config directory to load |
| `-v`, `--verbose` | `false` | Show resolved resource counts on pass |

## Test File Format

Test files use the `.test.vgo` extension and are placed in `policy/stage/tests/`. They are YAML files with these fields:

```yaml
name: descriptive test name
envoy: hostname-to-simulate

# Optional: mock traits for template rendering
traits:
  os:
    family: debian
  hardware:
    memory_mb: 16384

# Assertions
expect:
  module_count: 3
  resource_count: 12
  modules:
    - nginx
    - monitoring
  vars:
    nginx_port: "443"
    environment: production
  resources:
    - resource: nginx-package
      type: package
      package: nginx
    - resource: nginx-config
      type: file
      target_path: /etc/nginx/nginx.conf
      content_contains: "worker_connections 4096"
    - resource: nginx-service
      type: service
      service: nginx
      state: running
```

### Fields

| Field | Required | Description |
|-------|----------|-------------|
| `name` | No | Test name (defaults to filename) |
| `envoy` | Yes | Hostname to simulate check-in for |
| `traits` | No | Mock traits map for template rendering |
| `expect.module_count` | No | Expected number of modules |
| `expect.resource_count` | No | Expected total resource count across all modules |
| `expect.modules` | No | List of module names that must be present |
| `expect.vars` | No | Map of var name → expected string value |
| `expect.resources` | No | List of resource assertions (see below) |

### Resource Assertions

Each entry in `expect.resources` can check any combination of:

| Field | Description |
|-------|-------------|
| `resource` | Resource name (required) |
| `type` | Expected resource type (file, package, service, etc.) |
| `module` | Expected module name containing this resource |
| `targetpath` | Expected targetpath attribute |
| `package` | Expected package attribute |
| `service` | Expected service attribute |
| `state` | Expected state (defaults to "present" if not set on resource) |
| `when` | Expected when expression |
| `content_contains` | Substring that must appear in rendered content |

## Examples

### Test a module produces the right resources

```yaml
name: nginx module installs package and manages service
envoy: web01.prod.example.com

expect:
  modules: [nginx]
  resources:
    - resource: nginx-package
      type: package
      package: nginx
    - resource: nginx-service
      type: service
      state: running
```

### Test variable overrides

```yaml
name: production web servers get 443 and 16 workers
envoy: web01.prod.example.com

expect:
  vars:
    nginx_port: "443"
    workers: "16"
```

### Test template rendering with mock traits

```yaml
name: config renders hostname from traits
envoy: web01.prod.example.com
traits:
  hostname: web01.prod.example.com
  os:
    family: debian

expect:
  resources:
    - resource: prometheus-config
      content_contains: "host: web01.prod.example.com"
```

### Test role composition

```yaml
name: webserver role includes base security
envoy: web01.prod.example.com

expect:
  modules:
    - sshd-config     # from base role (included by webserver)
    - ntp             # from base role
    - nginx           # from webserver role directly
    - logrotate       # from webserver role directly
```

### Test conditional modules

```yaml
name: container envoys skip monitoring agent
envoy: container01.example.com

expect:
  modules:
    - nginx
  # node-exporter should NOT be in the list (when: "!is_container")
  module_count: 2
```

## Output

```
$ vigogo test
  PASS   nginx module installs package and manages service
  PASS   production web servers get 443 and 16 workers
  FAIL   container envoys skip monitoring agent
           module_count: got 3, want 2
  PASS   webserver role includes base security

3 passed, 1 failed, 0 errors (4 total)
```

Exit code is 1 if any tests fail.

## How It Works

1. Loads the full config tree from `--config-dir` (modules, roles, envoys, vars)
2. For each test file, simulates a check-in for the given `envoy` hostname
3. Resolves config using the same code path as the server (role expansion, module ordering, var merging, `environment_overrides`)
4. Compares resolved modules, resources, and vars against `expect` assertions
5. If `traits` are provided, renders `content:` templates before checking `content_contains`

Secrets are not resolved during tests (a null provider is used). Resources with `secret:` values will have empty strings.

`when:` expressions are not evaluated during tests — they remain as strings on the resolved resources. Use `expect.resources[].when` to assert the expression itself, not its result. Agent-side evaluation (which depends on real traits) is not simulated.

## Related

- [validate](validate.md) — check YAML syntax and idempotency
- [preview](preview.md) — preview planned changes against live envoys
- [Composition Patterns](../concepts/composition-patterns.md) — config reuse patterns
