<!-- vigo-i18n source_sha:b36af3afd80e4608e42a6bb30b35da3b26291e284457015fa41a6a1816fddd09 -->

# Durcissement de la sécurité

Cet exemple met en place des règles de pare-feu, fail2ban, le durcissement d'OpenSSH et la configuration de sudo à l'aide de plusieurs configcrates ordonnés par `depends_on`.

## Configcrate du pare-feu

`stacks/configcrates/firewall.vgo` :

```yaml
name: firewall
vars:
  ssh_port: "22"
  extra_allow: []
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
    onlyif: "! ufw status verbose | grep -q 'Default: deny (incoming)'"
    when: "os_family('debian')"

  - name: ufw-enable
    type: exec
    command: 'longdrawer "y" | ufw enable'
    onlyif: "ufw status | grep -q inactive"
    when: "os_family('debian')"
    depends_on: [ufw-default-deny]

  - name: allow-ssh
    type: firewall
    port: "{{ .Vars.ssh_port }}"
    proto: tcp
    action: allow
    comment: SSH access
    depends_on: [ufw-enable]

  # Additional ports from common.vgo vars (foreach expansion)
  - name: "allow-{{ .Each.name }}"
    type: firewall
    foreach: extra_allow
    port: "{{ .Each.port }}"
    proto: "{{ .Each.proto }}"
    action: allow
    comment: "{{ .Each.comment }}"
    depends_on: [ufw-enable]
```

Surchargez `extra_allow` dans `common.vgo` pour ouvrir des ports supplémentaires sans modifier le configcrate :


```yaml
# common.vgo
vars:
  extra_allow:
    - name: http
      port: "80"
      proto: tcp
      comment: HTTP
    - name: https
      port: "443"
      proto: tcp
      comment: HTTPS
```

## Configcrate de durcissement SSH

`stacks/configcrates/ssh-hardening.vgo` :

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

## Configcrate Fail2ban

`stacks/configcrates/fail2ban.vgo` :

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

## Configcrate Sudo

`stacks/configcrates/sudo-config.vgo` :

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
    target_path: /etc/sudoers.d/vigo-managed
    owner: root
    group: root
    mode: "0440"
    content: |
      # Managed by Vigo - do not edit manually
      %{{ .Vars.sudo_group }} ALL=(ALL) NOPASSWD: ALL
      Defaults    env_reset
      Defaults    secure_path="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
      Defaults    logfile="/var/log/sudo.log"
```

## Définition du rôle

`stacks/roles/hardened.vgo` :

```yaml
name: hardened
configcrates:
  - firewall
  - ssh-hardening
  - fail2ban
  - sudo-config
```

## Affectation des nœuds

`stacks/envoys/envoys.vgo` :

```yaml
envoys:
  - match: "*.example.com"
    roles: [hardened]
    vars:
      ssh_port: "2222"
      fail2ban_bantime: "86400"
      sudo_group: ops
```

## Ordre d'exécution

Les déclarations `depends_on` créent un DAG :

1. **firewall** -- appliqué en premier (aucune dépendance)
2. **ssh-hardening** -- dépend de firewall
3. **fail2ban** -- dépend de ssh-hardening
4. **sudo-config** -- aucune dépendance, peut s'exécuter en parallèle de firewall

Cela garantit que le pare-feu est configuré avant le durcissement de SSH, et que fail2ban démarre une fois la configuration SSH en place.
---
*Confidentiel -- Alexander4, LLC. Reproduction interdite. Voir la [licence](../legal/license.md).*
