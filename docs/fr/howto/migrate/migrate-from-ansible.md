<!-- vigo-i18n source_sha:3440d686d3d20c83200ec17aead829f27ad126e43a0197f998f97edb8b1dd857 -->

# Migration depuis Ansible

Ansible pousse la configuration via SSH depuis un nœud de contrôle ; Vigo exécute un agent persistant sur chaque Envoy qui tire l'état désiré depuis un serveur central. Les changements pratiques les plus importants lors du portage d'une configuration Ansible : le modèle d'exécution (push → pull), la portée des templates (Jinja2 partout → templates Go uniquement dans `content:`) et la forme de l'inventaire (`host_vars` / `group_vars` → hostcrates + rôles + vars par hôte).

## Différences d'architecture

Ansible pousse la configuration via SSH depuis un nœud de contrôle. Il n'y a pas d'agent persistant — chaque exécution se connecte, exécute les tâches, puis se déconnecte. Vigo exécute un agent persistant sur chaque nœud géré, qui tire l'état désiré depuis un serveur central via gRPC/mTLS à un intervalle configurable.

Ansible traite les tâches séquentiellement dans l'ordre du playbook. Vigo construit un DAG de ressources à partir des déclarations `depends_on`, `notify` et `subscribes`, puis exécute les ressources en respectant le graphe de dépendances.

Ansible est sans agent mais nécessite un accès SSH et Python sur les hôtes cibles. L'agent de Vigo est un unique binaire Rust statique sans dépendances d'exécution — il gère son propre TLS, la mise en cache d'état et la convergence hors ligne.

Ansible utilise Jinja2 partout — dans les templates, les arguments de tâches, les conditionnels, les fichiers de variables. Vigo restreint les templates Go aux seuls attributs `content:`. Tous les autres champs sont des valeurs littérales.

## Correspondance des concepts

| Concept Ansible | Équivalent Vigo | Notes |
|---|---|---|
| Playbook | `envoys.vgo` + rôles | L'affectation nœud-vers-rôle remplace le ciblage du playbook |
| Role | Configcrate + Role | Un rôle Ansible correspond à un configcrate Vigo ; un rôle Vigo regroupe des configcrates |
| Task | Resource | Unité de travail déclarative et idempotente |
| Handler | `notify` / `subscribes` | Mêmes sémantiques — déclenché sur changement, exécuté à la fin |
| Inventory | `envoys.vgo` | Motifs glob au lieu de groupes d'hôtes |
| `host_vars` | `vars:` au niveau du nœud | Surcharges par hôte dans `envoys.vgo` |
| `group_vars` | `vars:` de configcrate / `vars:` de rôle | Valeurs par défaut au niveau configcrate ou rôle |
| Facts (`ansible_*`) | Traits | Découverts automatiquement par l'agent |
| `gather_facts` | Collecte de traits toujours active | Traits collectés à chaque check-in — aucune bascule |
| Template (`.j2`) | Attribut `content:` | Template Go en ligne, pas de fichier séparé |
| `when:` | `when:` | Même mot-clé, syntaxe différente (pas de Jinja2) |
| `loop` / `with_items` | `foreach:` | Itère sur une liste ou une map pour générer des ressources |
| `become: yes` | L'agent s'exécute en root | Pas de sudo — l'agent s'exécute en root par conception |
| `register:` | Aucun équivalent | Les ressources ne capturent pas de sortie pour un usage ultérieur |
| Vault | Préfixe `secret:` | Résolu côté serveur lors de la compilation du bundle |
| `ansible-pull` | Mode par défaut | Vigo fonctionne toujours en pull — c'est le modèle principal |
| Galaxy role | Fichier `.vgo` de configcrate | Pas de registre de paquets — fichiers YAML sous contrôle de version |
| `tags:` | Aucun équivalent | Toutes les ressources d'un configcrate s'appliquent toujours (utilisez `when:` pour ignorer conditionnellement) |
| `delegate_to:` | Tasks / Workflows | L'orchestration inter-nœuds passe par le système de tâches |
| Mode `--check` | Dry-run en phase de vérification | Chaque ressource vérifie avant d'agir — inhérent au modèle |

## Correspondance des types de ressources

| Module Ansible | Exécuteur Vigo | Notes |
|---|---|---|
| `ansible.builtin.package` | `package` | Détection automatique du gestionnaire de paquets |
| `ansible.builtin.copy` / `template` | `file` | `content:` pour les templates, `sourcepath:` pour les copies |
| `ansible.builtin.service` | `service` | `state: running/stopped`, `enabled: true/false` |
| `ansible.builtin.command` / `shell` | `exec` | `command:`, `onlyif:`, `unless:` |
| `ansible.builtin.user` | `user` | `username:`, `uid:`, `shell:`, `groups:` |
| `ansible.builtin.group` | `group` | `groupname:`, `gid:`, `members:` |
| `ansible.builtin.cron` | `cron` | `command:`, `minute:`, `hour:`, etc. |
| `ansible.builtin.lineinfile` | `file_line` | `path:`, `line:`, `match:` |
| `ansible.builtin.apt_repository` | `repository` | Unifié entre distributions |
| `ansible.builtin.get_url` / `unarchive` | `source_package` | `url:`, `target_path:`, `checksum:` (tarballs/zips) |
| `ansible.builtin.apt` (avec `deb:`) / `dnf` (avec URL) | `nonrepo_package` | Installe des .deb/.rpm/.pkg/.msi depuis des URLs |
| `ansible.posix.mount` | `mount` | `device:`, `mountpoint:`, `fstype:` |
| `ansible.posix.sysctl` | `sysctl` | `key:`, `value:`, `state: present` |
| `ansible.builtin.hostname` | `hostname` | `hostname:` |
| `ansible.builtin.blockinfile` | `blockinfile` | `path:`, `block:`, `marker:` |

## Pas à pas : conversion d'un rôle Nginx


### Version Ansible

`roles/nginx/defaults/main.yml` :

```yaml
nginx_worker_processes: auto
nginx_worker_connections: 1024
nginx_server_name: localhost
nginx_root: /var/www/html
```

`roles/nginx/tasks/main.yml` :

```yaml
- name: Install nginx
  ansible.builtin.package:
    name: nginx
    state: present

- name: Configure nginx
  ansible.builtin.template:
    src: nginx.conf.j2
    dest: /etc/nginx/nginx.conf
    owner: root
    group: root
    mode: "0644"
  notify: Restart nginx

- name: Ensure nginx is running
  ansible.builtin.service:
    name: nginx
    state: started
    enabled: true
```

`roles/nginx/handlers/main.yml` :

```yaml
- name: Restart nginx
  ansible.builtin.service:
    name: nginx
    state: restarted
```

`roles/nginx/templates/nginx.conf.j2` :

```jinja2
worker_processes {{ nginx_worker_processes }};
events {
    worker_connections {{ nginx_worker_connections }};
}
http {
    include       /etc/nginx/mime.types;
    default_type  application/octet-stream;
    sendfile      on;
    keepalive_timeout 65;

    server {
        listen 80;
        server_name {{ nginx_server_name }};
        root {{ nginx_root }};

        location / {
            try_files $uri $uri/ =404;
        }
    }
}
```

### Version Vigo

`stacks/configcrates/nginx.vgo` :

```yaml
name: nginx
vars:
  nginx_worker_processes: auto
  nginx_worker_connections: 1024
  nginx_server_name: localhost
  nginx_root: /var/www/html
resources:
  - name: nginx-package
    type: package
    package: nginx
    state: present

  - name: nginx-config
    type: file
    target_path: /etc/nginx/nginx.conf
    owner: root
    group: root
    mode: "0644"
    content: |
      worker_processes {{ .Vars.nginx_worker_processes }};
      events {
          worker_connections {{ .Vars.nginx_worker_connections }};
      }
      http {
          include       /etc/nginx/mime.types;
          default_type  application/octet-stream;
          sendfile      on;
          keepalive_timeout 65;

          server {
              listen 80;
              server_name {{ .Vars.nginx_server_name }};
              root {{ .Vars.nginx_root }};

              location / {
                  try_files $uri $uri/ =404;
              }
          }
      }
    notify:
      - nginx-service

  - name: nginx-service
    type: service
    service: nginx
    state: running
    enabled: true
```

`stacks/roles/webserver.vgo` :

```yaml
name: webserver
configcrates:
  - nginx
```

`stacks/envoys/envoys.vgo` :

```yaml
envoys:
  - match: "*.web.example.com"
    environment: production
    roles: [webserver]
    vars:
      nginx_worker_processes: "4"
      nginx_worker_connections: "4096"
      nginx_server_name: "example.com"
      nginx_root: /var/www/example.com
```

### Différences clés

- **Templates en ligne ou dans `templates/`.** Les petits templates vont en ligne dans `content:`. Les fichiers plus volumineux utilisent `source: templates/nginx.conf.tmpl` — le serveur lit et rend le fichier depuis le répertoire `templates/`. Même concept que le `templates/` d'Ansible, mais sans Jinja2.
- **Pas de répertoire handlers.** Le motif handler/notify d'Ansible correspond directement à la liste `notify:` de la ressource dans Vigo. Pas de fichier `handlers/main.yml` séparé.
- **Pas de répertoire defaults.** Les `vars:` au niveau du configcrate remplacent `defaults/main.yml`. Même concept, en ligne dans le fichier du configcrate.
- **Un fichier par configcrate.** Un rôle Ansible compte 4 fichiers ou plus répartis sur plusieurs répertoires. Un configcrate Vigo est un unique fichier `.vgo`.
- **Syntaxe des templates.** `{{ variable }}` en Jinja2 devient `{{ .Vars.variable }}` dans les templates Go. Notez le préfixe `.Vars.`.

## Hiérarchie des variables et des données

Ansible possède une [précédence des variables](https://docs.ansible.com/ansible/latest/playbook_guide/playbooks_variables.html#understanding-variable-precedence) réputée complexe, avec 22 niveaux. Vigo en a trois :

1. **Valeurs par défaut du configcrate** — `vars:` dans le fichier `.vgo` du configcrate
2. **Vars héritées de common.vgo** — partagées dans le sous-arbre du répertoire
3. **Vars du bloc match** — `vars:` dans l'entrée `envoys.vgo`
4. **Vars de `environments.vgo`** — sélectionnées par le `environment:` du bloc match

Les couches ultérieures surchargent les précédentes. Les vars spécifiques à un environnement vivent dans un fichier voisin, pas en ligne dans le bloc match :

```yaml
# envoys.vgo
envoys:
  - match: "prod-*.web.example.com"
    environment: production
    roles: [webserver]
  - match: "stg-*.web.example.com"
    environment: staging
    roles: [webserver]

# environments.vgo
env:
  production:
    vars:
      nginx_server_name: "example.com"
  staging:
    vars:
      nginx_server_name: "staging.example.com"
```

Pour des données partagées entre de nombreux blocs match, utilisez `common.vgo` dans un répertoire parent — l'héritage de répertoire remplace les motifs `group_vars/` + `vars_files:` d'Ansible.

## Templates

Ansible utilise des templates Jinja2 partout. Vigo utilise des templates Go uniquement dans `content:`.


| Jinja2 Ansible | Template Go Vigo |
|---|---|
| `{{ variable }}` | `{{ .Vars.variable }}` |
| `{{ ansible_hostname }}` | `{{ .Traits.identity.hostname }}` |
| `{{ ansible_os_family }}` | `{{ .Traits.os.family }}` |
| `{% if condition %}` | `{{ if .Vars.condition }}` |
| `{% for item in list %}` | `{{ range .Vars.list }}` |
| `{{ variable \| lower }}` | `{{ lower .Vars.variable }}` |
| `{{ variable \| default('x') }}` | `{{ default .Vars.variable "x" }}` |

**Restriction critique :** les templates Vigo ne fonctionnent qu'à l'intérieur des attributs `content:`. Vous ne pouvez pas utiliser `{{ }}` dans `package:`, `target_path:`, `command:` ou tout autre champ. Pour un comportement dynamique dans les champs hors `content`, utilisez des expressions `when:` avec des ressources distinctes.

## Conditionnels et ciblage

Les conditionnels Ansible utilisent la syntaxe Jinja2 :

```yaml
- name: Install nginx (Debian)
  ansible.builtin.apt:
    name: nginx
    state: present
  when: ansible_os_family == "Debian"
```

Vigo utilise `when:` avec des fonctions intégrées :

```yaml
- name: nginx-package-debian
  type: package
  package: nginx
  state: present
  when: "os_family('debian')"

- name: nginx-package-redhat
  type: package
  package: nginx
  state: present
  when: "os_family('redhat')"
```

Le `when:` d'Ansible accepte des expressions Jinja2 complètes. Le `when:` de Vigo utilise sa propre syntaxe d'expression avec 15 fonctions intégrées et des opérateurs booléens (`&&`, `||`, `!`).

## Secrets

Ansible Vault chiffre des fichiers entiers ou des variables individuelles :

```bash
ansible-vault encrypt_string 'supersecret' --name 'db_password'
```

Vigo utilise le préfixe `secret:` dans n'importe quelle valeur :

```yaml
content: "secret:vigo/nginx/ssl_key"
```

Les secrets sont résolus côté serveur lors de la compilation du bundle. La valeur chiffrée n'apparaît jamais dans les configs, les logs ou le trafic de l'agent. Aucun fichier de mot de passe de vault à gérer.

## Pièges courants


1. **Pas de mode push.** Ansible pousse à la demande. Les agents Vigo tirent à intervalle régulier. Pour une convergence immédiate, utilisez `vigocli envoys push` pour relancer des Envoys spécifiques, ou `vigocli task run` pour des commandes ad-hoc.

2. **Pas de `register:` / chaînage de variables.** Ansible peut capturer la sortie d'une tâche et la réutiliser dans des tâches ultérieures. Les ressources Vigo sont déclaratives et ne produisent pas de variables. Si vous avez besoin de valeurs dynamiques, utilisez les traits (pour les faits système) ou l'héritage `common.vgo` au niveau du répertoire (pour les données partagées).

3. **Templates uniquement dans `content:`.** Le Jinja2 d'Ansible fonctionne partout — arguments de tâches, `when:`, fichiers de variables. Vigo restreint les templates à `content:`. Résistez à la tentation de templater `target_path:` ou `command:`.

4. **Pas d'ordonnancement des tâches par position dans le fichier.** Ansible exécute les tâches dans l'ordre où elles apparaissent. Vigo construit un DAG — sans `depends_on` ou `notify` explicites, les ressources peuvent s'exécuter dans n'importe quel ordre. Déclarez toujours les dépendances explicitement.

5. **Pas de `become:` / sudo.** L'agent Vigo s'exécute en root. Il n'y a pas de modèle d'élévation de privilèges — si l'agent doit le faire, il le fait en root.

6. **Pas de `--limit` ni de `--tags`.** Ansible peut cibler des sous-ensembles d'hôtes ou de tâches par exécution. Vigo applique toutes les ressources correspondantes à chaque check-in. Utilisez `when:` pour ignorer conditionnellement des ressources, et des motifs glob dans `envoys.vgo` pour le ciblage des hôtes.

7. **Pas de commande `ansible` ad-hoc.** Pour des commandes ponctuelles sur le parc, utilisez `vigocli task run` (ou `vigocli traits livequery` pour lire un trait) — celles-ci passent par le flux de l'agent.

8. **L'agent doit d'abord être installé.** Ansible n'a besoin que de SSH. Vigo nécessite l'amorçage du binaire de l'agent sur chaque nœud. Utilisez `curl -sSfk https://<server>:8443/bootstrap | sudo sh` pour un enrôlement automatisé.

## Voir aussi

- [Format de configuration](../../reference/configcrate-language.md) — Configcrates, rôles, envoys.vgo, vars
- [DAG de ressources](../../reference/configcrate-language.md) — depends_on, notify, subscribes
- [Templates](../../reference/configcrate-language.md) — Syntaxe et restrictions des templates Go
- [Expressions When](../../reference/configcrate-language.md) — Expressions conditionnelles et fonctions intégrées
- [Secrets](../../reference/server-yaml.md) — Fournisseurs de secrets et le préfixe secret:
- [Exécuteurs](../../reference/vigo/resources/_index.md) — Tous les exécuteurs de ressources
- [Exemple de Configcrate Nginx](../../examples/nginx-module.md) — Pas à pas complet de nginx
- [Premier Configcrate](../../howto/configcrates/write-your-first-configcrate.md) — Écrire votre premier configcrate

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*
