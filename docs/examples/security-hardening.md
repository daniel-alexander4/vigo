# Security Hardening

This example sets up firewall rules, fail2ban, OpenSSH hardening, and sudo configuration using multiple modules with `depends_on` ordering.

## Firewall Module

`policy/stage/modules/firewall.hnb`:

```yaml
name: firewall
vars:
  ssh_port: "22"
  allowed_ports: "22,80,443"
resources:
  - name: ufw-package
    type: package
    package: ufw
    state: present
    when: "os_family('debian')"

  - name: firewall-package
    type: package
    package: firewalld
    state: present
    when: "!os_family('debian')"

  - name: ufw-default-deny
    type: exec
    command: "ufw default deny incoming"
    unless: "ufw status | grep -q 'Default: deny'"
    when: "os_family('debian')"

  - name: ufw-allow-ssh
    type: exec
    command: "ufw allow {{ .Vars.ssh_port }}/tcp"
    unless: "ufw status | grep -q '{{ .Vars.ssh_port }}/tcp'"
    when: "os_family('debian')"

  - name: ufw-allow-http
    type: exec
    command: "ufw allow 80/tcp"
    unless: "ufw status | grep -q '80/tcp'"
    when: "os_family('debian')"

  - name: ufw-allow-https
    type: exec
    command: "ufw allow 443/tcp"
    unless: "ufw status | grep -q '443/tcp'"
    when: "os_family('debian')"

  - name: ufw-enable
    type: exec
    command: "echo 'y' | ufw enable"
    unless: "ufw status | grep -q 'Status: active'"
    when: "os_family('debian')"
```

## SSH Hardening Module

`policy/stage/modules/ssh-hardening.hnb`:

```yaml
name: ssh-hardening
depends_on:
  - firewall
vars:
  ssh_port: "22"
  ssh_permit_root: "no"
  ssh_password_auth: "no"
  ssh_max_auth_tries: "3"
resources:
  - name: sshd-config
    type: file
    target_path: /etc/ssh/sshd_config.d/99-hardening.conf
    owner: root
    group: root
    mode: "0644"
    content: |
      Port {{ .Vars.ssh_port }}
      PermitRootLogin {{ .Vars.ssh_permit_root }}
      PasswordAuthentication {{ .Vars.ssh_password_auth }}
      MaxAuthTries {{ .Vars.ssh_max_auth_tries }}
      X11Forwarding no
      AllowTcpForwarding no
      ClientAliveInterval 300
      ClientAliveCountMax 2
    notify:
      - sshd-service

  - name: sshd-service
    type: service
    service: sshd
    state: running
    enabled: true
```

## Fail2ban Module

`policy/stage/modules/fail2ban.hnb`:

```yaml
name: fail2ban
depends_on:
  - ssh-hardening
vars:
  fail2ban_bantime: "3600"
  fail2ban_maxretry: "5"
resources:
  - name: fail2ban-package
    type: package
    package: fail2ban
    state: present

  - name: fail2ban-jail-config
    type: file
    target_path: /etc/fail2ban/jail.local
    owner: root
    group: root
    mode: "0644"
    content: |
      [DEFAULT]
      bantime = {{ .Vars.fail2ban_bantime }}
      maxretry = {{ .Vars.fail2ban_maxretry }}
      backend = systemd

      [sshd]
      enabled = true
      port = {{ .Vars.ssh_port }}
    notify:
      - fail2ban-service

  - name: fail2ban-service
    type: service
    service: fail2ban
    state: running
    enabled: true
```

## Sudo Module

`policy/stage/modules/sudo-config.hnb`:

```yaml
name: sudo-config
vars:
  sudo_group: admin
resources:
  - name: sudo-package
    type: package
    package: sudo
    state: present

  - name: sudoers-config
    type: file
    target_path: /etc/sudoers.d/hobnob-managed
    owner: root
    group: root
    mode: "0440"
    content: |
      # Managed by Hobnob - do not edit manually
      %{{ .Vars.sudo_group }} ALL=(ALL) NOPASSWD: ALL
      Defaults    env_reset
      Defaults    secure_path="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
      Defaults    logfile="/var/log/sudo.log"
```

## Role Definition

`policy/stage/roles/hardened.hnb`:

```yaml
name: hardened
modules:
  - firewall
  - ssh-hardening
  - fail2ban
  - sudo-config
```

## Node Assignment

`policy/stage/thralls/nodes.hnb`:

```yaml
thralls:
  - match: "*.example.com"
    roles: [hardened]
    vars:
      ssh_port: "2222"
      fail2ban_bantime: "86400"
      sudo_group: ops
```

## Execution Order

The `depends_on` declarations create a DAG:

1. **firewall** -- applied first (no dependencies)
2. **ssh-hardening** -- depends on firewall
3. **fail2ban** -- depends on ssh-hardening
4. **sudo-config** -- no dependencies, can run in parallel with firewall

This ensures the firewall is configured before SSH is hardened, and fail2ban starts after SSH configuration is in place.
