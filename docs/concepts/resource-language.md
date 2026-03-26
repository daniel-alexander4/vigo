
# Resource Language

Beyond basic resource definitions, Vigo supports several advanced config patterns for reducing repetition and expressing complex desired state.

## defaults

Apply default attributes to all resources in a module:

```yaml
name: web-configs
defaults:
  owner: www-data
  group: www-data
  mode: "0644"

resources:
  - name: index-html
    type: file
    target_path: /var/www/html/index.html
    content: "<h1>Hello</h1>"
    # owner, group, mode inherited from defaults

  - name: error-page
    type: file
    target_path: /var/www/html/error.html
    content: "<h1>Error</h1>"
    # also inherits defaults
```

Resources can override defaults by specifying the attribute explicitly.

## foreach

Iterate over a variable to create multiple resources from a single definition:

```yaml
name: user-management

vars:
  users:
    - name: alice
      shell: /bin/bash
    - name: bob
      shell: /bin/zsh

resources:
  - name: "user-{{ .Item.name }}"
    type: user
    username: "{{ .Item.name }}"
    shell: "{{ .Item.shell }}"
    state: present
    foreach: users
```

This expands at config load time into:

```yaml
resources:
  - name: user-alice
    type: user
    username: alice
    shell: /bin/bash
  - name: user-bob
    type: user
    username: bob
    shell: /bin/zsh
```

The `foreach` field references a variable that must be a list. For string lists, each value is available as `{{ .Item }}`. For list-of-maps, access fields with `{{ .Each.key }}` (or `{{ .Item.key }}`):

```yaml
# Firewall rules from a list of maps
vars:
  extra_allow: []   # override in common.vgo

resources:
  - name: "allow-{{ .Each.name }}"
    type: firewall
    foreach: extra_allow
    port: "{{ .Each.port }}"
    proto: "{{ .Each.proto }}"
    action: allow
    comment: "{{ .Each.comment }}"
```

```yaml
# common.vgo — operators configure here
vars:
  extra_allow:
    - name: https
      port: "443"
      proto: tcp
      comment: HTTPS
    - name: app
      port: "8080"
      proto: tcp
      comment: App server
```

Empty lists produce zero resources — canonical modules stay untouched.

## case / match

Select attribute overrides based on a template expression:

```yaml
resources:
  - name: package-manager-config
    type: file
    target_path: /etc/package-manager.conf
    case: "{{ .Traits.os.family }}"
    match:
      debian:
        content: "manager=apt"
        target_path: /etc/apt/apt.conf.d/99-custom
      redhat:
        content: "manager=yum"
        target_path: /etc/yum.conf.d/custom.conf
```

The `case` expression is evaluated, and the matching `match` branch's attributes override the resource's base attributes. If no branch matches, the resource uses its base attributes (or is skipped if no base `content` exists).

## conditional_block

Group resources under a shared `when:` expression:

```yaml
resources:
  - conditional_block:
      when: "os_family('debian')"
      resources:
        - name: apt-update
          type: exec
          command: "apt-get update"

        - name: build-tools
          type: package
          package: build-essential

        - name: dev-headers
          type: package
          package: linux-headers-generic
```

At config load time, this is flattened. Each child resource gets the block's `when:` composed with any existing `when:` via AND:

```yaml
# Equivalent to:
- name: apt-update
  type: exec
  command: "apt-get update"
  when: "os_family('debian')"

- name: build-tools
  type: package
  package: build-essential
  when: "os_family('debian')"

- name: dev-headers
  type: package
  package: linux-headers-generic
  when: "os_family('debian')"
```

If a child resource already has a `when:`, the block's expression is ANDed:

```yaml
- conditional_block:
    when: "os_family('debian')"
    resources:
      - name: special
        type: package
        package: special-tool
        when: "arch('amd64')"
        # effective when: "os_family('debian') && arch('amd64')"
```

## state: absent

Every executor supports `state: absent` to remove/disable the managed resource:

```yaml
- name: remove-old-package
  type: package
  package: legacy-tool
  state: absent

- name: remove-old-config
  type: file
  target_path: /etc/legacy/config.yaml
  state: absent

- name: disable-old-service
  type: service
  service: legacy-daemon
  state: stopped
  enabled: false
```

## Combining Patterns

These features compose naturally:

```yaml
name: multi-os-packages

vars:
  debian_packages: [nginx, curl, jq]
  redhat_packages: [nginx, curl, jq]

resources:
  - conditional_block:
      when: "os_family('debian')"
      resources:
        - name: "pkg-{{ .Item }}"
          type: package
          package: "{{ .Item }}"
          foreach: debian_packages

  - conditional_block:
      when: "os_family('redhat')"
      resources:
        - name: "pkg-{{ .Item }}"
          type: package
          package: "{{ .Item }}"
          foreach: redhat_packages
```

## stream_edit

Pipe file content through scripts on the envoy before writing. Accepts a single path or a list for chained transforms:

```yaml
resources:
  - name: deploy-filter
    type: file
    target_path: /usr/local/bin/strip-comments.sh
    content: |
      #!/bin/sh
      grep -v '^[[:space:]]*#'
    mode: "0755"

  - name: clean-config
    type: file
    target_path: /etc/myapp/config.conf
    content: |
      # database settings
      host = localhost
      port = 5432
    stream_edit: /usr/local/bin/strip-comments.sh
    depends_on: [deploy-filter]
```

Each script reads stdin and writes to stdout. Non-zero exit fails the resource. See [file executor: stream_edit](../reference/executors/file.md#stream-edit) for full reference.

## timeout

Every resource has an implicit execution timeout. If the executor doesn't complete within the limit, the resource fails with "execution timed out." Override per-resource with the `timeout` parameter (seconds):

```yaml
- name: install-large-package
  type: package
  package: texlive-full
  timeout: 600    # 10 minutes
```

Default timeouts by resource type:

| Type | Default |
|------|---------|
| `exec`, `archive`, `custom`, `package`, `repository` | 300s (5 min) |
| All others (`file`, `service`, `user`, etc.) | 60s |

## Related

- [Config Format](config-format.md) — Module and variable structure
- [Resource DAG](resource-dag.md) — Dependency ordering
- [When Expressions](when-expressions.md) — Conditional logic
