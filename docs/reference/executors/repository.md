
# repository

Manages OS package repository configurations. Supports APT (Debian/Ubuntu) and DNF/YUM (RHEL/Fedora) backends.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `repository` | Yes | -- | Repository identifier name. |
| `state` | No | `present` | Desired state: `present` or `absent`. |
| `repo` | Yes (for present) | -- | Repository URL or definition string. For APT: a deb line (e.g., `deb https://... stable main`). For DNF/YUM: the baseurl. |
| `key_url` | No | -- | URL to the GPG signing key for the repository. Downloaded and imported automatically. |

## States

- **`present`** -- Ensure the repository is configured. On APT, writes to `/etc/apt/sources.list.d/<name>.list`. On DNF/YUM, writes to `/etc/yum.repos.d/<name>.repo`.
- **`absent`** -- Remove the repository configuration file.

## Idempotency

Checks if the repository configuration file exists and whether its content matches the desired configuration. Only writes when there is drift.

## Examples

### APT repository with GPG key

```yaml
resources:
  - name: docker-ce
    type: repository
    repository: docker-ce
    repo: "deb [arch=amd64] https://download.docker.com/linux/ubuntu jammy stable"
    key_url: https://download.docker.com/linux/ubuntu/gpg
```

### DNF/YUM repository

```yaml
resources:
  - name: epel
    type: repository
    repository: epel
    repo: "https://dl.fedoraproject.org/pub/epel/9/Everything/x86_64/"
```

### Remove a repository

```yaml
resources:
  - name: old-repo
    type: repository
    repository: old-repo
    state: absent
```

### With depends_on

```yaml
resources:
  - name: docker-repo
    type: repository
    repository: docker-ce
    repo: "deb [arch=amd64] https://download.docker.com/linux/ubuntu jammy stable"
    key_url: https://download.docker.com/linux/ubuntu/gpg

  - name: docker-ce
    type: package
    package: docker-ce
    depends_on: docker-repo
```

## Platform

Cross-platform (compiles everywhere) but operationally Linux-specific. The backend is selected based on the OS family reported by the agent.

## GPG Key Handling

APT repositories use the modern `signed-by` keyring path (`/etc/apt/keyrings/<name>.gpg`). The legacy `apt-key add` path is never used.

| Scenario | Behavior |
|----------|----------|
| Key URL provided | Downloaded via curl (wget fallback), dearmored to binary GPG format |
| Key expired (`EXPKEYSIG`) | Automatically re-fetched from `key_url` if configured |
| Key missing (`NO_PUBKEY`) | Reports error with the missing fingerprint — add `key_url` to fix |
| Key URL returns 404 | Reports error with the failed URL — update `key_url` in the module |
| Key download fails | Tries curl then wget, reports clear error if both fail |

When `apt-get update` fails after adding a repository, the executor parses the error and reports a classified message. GPG and repo errors feed into the package circuit breaker (see [package executor](package.md)).

## Notes

- For APT, GPG keys are stored in `/etc/apt/keyrings/<name>.gpg` and referenced via `signed-by` in the sources file. The legacy `apt-key` path is never used.
- For DNF/YUM, GPG keys are imported via `rpm --import` and the repo file includes `gpgcheck=1` and `gpgkey=<key_url>` when a key URL is provided.
- The executor auto-detects the package manager backend based on the OS family.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
