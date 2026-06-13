<!-- vigo-i18n source_sha:806b1ae9a40108444a0f8d2247b71c90208c03906cad27cc2460569a9731cb28 -->

# Migration depuis Salt

Salt et Vigo utilisent tous deux YAML pour définir l'état, disposent tous deux de faits système (grains Salt, traits Vigo) et prennent tous deux en charge un modèle d'agent en mode pull. Les principaux changements pratiques : le rendu des modèles (Jinja2 → modèles Go limités à `content:`), la recherche de données (pillar Salt → résolution `secret:` Vigo + vars de hostcrate + lookups) et le modèle d'exécution (commandes émises par le master Salt → convergence sur politique tirée de Vigo).

## Différences d'architecture

Salt prend en charge à la fois le push (par défaut via ZeroMQ) et le pull (`salt-call --local` ou salt-minion en mode `masterless`). Vigo est exclusivement en mode pull — les agents interrogent le serveur à un intervalle configurable via gRPC/mTLS.

Salt effectue le rendu des fichiers d'état via Jinja2 avant de les analyser comme YAML, ce qui signifie que n'importe quelle valeur YAML peut contenir de la logique Jinja2. Vigo garde le YAML strictement littéral — les modèles Go ne sont autorisés qu'à l'intérieur des attributs `content:`. Il s'agit d'une différence de conception fondamentale qui influe sur la façon dont vous structurez vos configs.

Le master Salt stocke les données pillar, orchestre via le bus d'événements et peut pousser vers les minions en temps réel. Le serveur Vigo compile les bundles à la demande lors de chaque check-in. Pour les opérations en temps réel, Vigo utilise le système de répartition des tâches et des requêtes via les flux d'agents.

Salt nécessite Python à la fois sur le master et sur le minion. Le serveur Vigo est en Go (vigosrv) et l'agent est un unique binaire Rust statique — aucune dépendance d'exécution sur les nœuds gérés.

## Correspondance des concepts

| Concept Salt | Équivalent Vigo | Notes |
|---|---|---|
| Fichier d'état (`.sls`) | Configcrate (fichier `.vgo`) | Tous deux en YAML, règles de rendu différentes |
| `top.sls` | `envoys.vgo` | Correspondance par glob → attribution de rôle/configcrate |
| Formula | Configcrate | Ensemble d'état réutilisable — même concept |
| Pillar | `vars` / `common.vgo` / `environments.vgo` | Données côté serveur pour les nœuds, en couches via configcrate → héritage → match → env |
| Grains | Traits | Faits système découverts par l'agent |
| Mine | Aucun équivalent | Pas de partage de données entre agents |
| Requisite (`require`) | `depends_on` | Ordonnancement du DAG |
| Requisite (`watch`) | `subscribes` | Réappliquer quand une dépendance change |
| Requisite (`require_in`) | `before` | Déclaration de dépendance inverse |
| Requisite (`listen`) | `notify` | Déclencher les ressources en aval |
| Jinja2 dans les états | Modèle Go uniquement dans `content:` | Vigo n'effectue pas de rendu de modèle dans la structure YAML |
| `salt-call --local` | Convergence hors ligne | L'agent utilise le bundle en cache quand le serveur est injoignable |
| `salt-run` | `vigocli task run` | Orchestration côté serveur |
| Bus d'événements Salt | Flux d'agent | Flux gRPC persistant pour la répartition en temps réel |
| `salt-cloud` | Aucun équivalent | Vigo ne provisionne pas d'infrastructure |
| Pillar GPG | Préfixe `secret:` | Secrets résolus côté serveur |
| Beacons | Aucun équivalent | L'agent n'émet pas d'événements |
| Reactor | Webhooks | Automatisation pilotée par événements via répartition de webhooks |
| Environments | `environments.vgo` (indexé par le `environment:` du bloc match) | Les surcharges spécifiques à l'environnement résident dans un fichier voisin |
| CLI `salt` | `vigocli` | CLI de gestion du serveur |
| `salt-minion` | Agent `vigo` | Binaire Rust, magasin d'état LMDB |
| `salt-master` | `vigosrv` | Binaire Go, gRPC + REST + UI web |

## Correspondance des types de ressources

| Module d'état Salt | Exécuteur Vigo | Notes |
|---|---|---|
| `pkg.installed` | `package` | Détecte automatiquement le gestionnaire de paquets |
| `file.managed` | `file` | `content:` ou `sourcepath:` |
| `service.running` | `service` | `state: running`, `enabled: true` |
| `cmd.run` | `exec` | `command:`, `onlyif:`, `unless:` |
| `user.present` | `user` | `username:`, `uid:`, `shell:`, `groups:` |
| `group.present` | `group` | `groupname:`, `gid:`, `members:` |
| `cron.present` | `cron` | `command:`, `minute:`, `hour:`, etc. |
| `pkgrepo.managed` | `repository` | Unifié entre distributions |
| `archive.extracted` | `source_package` | `url:`, `target_path:`, `checksum:` (tarballs/zips) |
| `pkg.installed` (avec URL `sources:`) | `nonrepo_package` | Installer des .deb/.rpm/.pkg/.msi depuis des URL |
| `mount.mounted` | `mount` | `device:`, `mountpoint:`, `fstype:` |
| `sysctl.present` | `sysctl` | `key:`, `value:` |
| `file.blockreplace` | `blockinfile` | `path:`, `block:`, `marker:` |
| `file.line` | `file_line` | `path:`, `line:`, `match:` |
| `host.present` | `host` | `hostname:`, `ip:` |


## Tutoriel : conversion d'un état Nginx

### Version Salt

`nginx/init.sls` :

```yaml
{% set nginx = salt['pillar.get']('nginx', {}) %}
{% set worker_processes = nginx.get('worker_processes', 'auto') %}
{% set worker_connections = nginx.get('worker_connections', 1024) %}
{% set server_name = nginx.get('server_name', 'localhost') %}
{% set root = nginx.get('root', '/var/www/html') %}

nginx-package:
  pkg.installed:
    - name: nginx

nginx-config:
  file.managed:
    - name: /etc/nginx/nginx.conf
    - source: salt://nginx/files/nginx.conf.j2
    - template: jinja
    - user: root
    - group: root
    - mode: '0644'
    - context:
        worker_processes: {{ worker_processes }}
        worker_connections: {{ worker_connections }}
        server_name: {{ server_name }}
        root: {{ root }}
    - require:
      - pkg: nginx-package
    - watch_in:
      - service: nginx-service

nginx-service:
  service.running:
    - name: nginx
    - enable: True
```

`nginx/files/nginx.conf.j2` :

```jinja2
worker_processes {{ worker_processes }};
events {
    worker_connections {{ worker_connections }};
}
http {
    include       /etc/nginx/mime.types;
    default_type  application/octet-stream;
    sendfile      on;
    keepalive_timeout 65;

    server {
        listen 80;
        server_name {{ server_name }};
        root {{ root }};
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

- **Pas de Jinja2 dans la structure YAML.** La plus grande force et le plus grand piège de Salt est le rendu Jinja2 du YAML lui-même. Vigo garde la structure YAML statique — les modèles ne fonctionnent qu'à l'intérieur des valeurs `content:`.
- **Pas de serveur de fichiers `salt://`.** Salt sert les modèles et fichiers via des URI `salt://`. Vigo utilise `content:` pour les modèles en ligne ou `source: templates/app.conf.tmpl` pour des fichiers de modèle externes — aucun serveur de fichiers nécessaire.
- **Les requisites se correspondent directement.** Les `require`/`watch`/`listen`/`require_in` de Salt correspondent proprement aux `depends_on`/`subscribes`/`notify`/`before` de Vigo. La sémantique est presque identique.
- **Pas de recherches pillar.get().** Les variables sont accessibles directement via `{{ .Vars.name }}` dans les modèles ou définies comme valeurs par défaut de configcrate — aucune fonction de recherche nécessaire.

## Hiérarchie des variables et des données

Les données pillar de Salt sont hiérarchiques et peuvent être fusionnées entre plusieurs sources. Vigo simplifie cela :

| Niveau Salt | Équivalent Vigo |
|---|---|
| Pillar (valeurs par défaut de formula) | `vars:` du configcrate |
| Pillar (partagé entre nœuds) | `common.vgo` dans les répertoires ancêtres (hérité vers le bas de l'arborescence) |
| Pillar (par environnement) | `environments.vgo` indexé par le `environment:` du bloc match |
| Pillar (par minion) | `vars:` du bloc match dans `envoys.vgo` |
| Grains | Traits (`.Traits` dans les modèles, fonctions intégrées dans `when:`) |
| Mine | Aucun équivalent — utilisez `common.vgo` pour les données partagées |

Les surcharges par environnement résident dans un fichier voisin, pas par bloc match :

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

## Modèles

Salt utilise Jinja2 à la fois dans les fichiers d'état et dans les fichiers de modèle. Vigo utilise les modèles Go uniquement dans `content:`.

| Jinja2 Salt | Modèle Go Vigo |

|---|---|
| `{{ variable }}` | `{{ .Vars.variable }}` |
| `{{ grains['os_family'] }}` | `{{ .Traits.os.family }}` |
| `{{ grains['fqdn'] }}` | `{{ .Traits.identity.fqdn }}` |
| `{% if condition %}` | `{{ if .Vars.condition }}` |
| `{% for item in list %}` | `{{ range .Vars.list }}` |
| `{{ salt['pillar.get']('key', 'default') }}` | `{{ default .Vars.key "default" }}` |
| `{{ variable \| lower }}` | `{{ lower .Vars.variable }}` |

**Restriction critique :** vous ne pouvez pas effectuer un rendu Jinja2 de la structure YAML dans Vigo. Les blocs `{% if %}` qui incluent conditionnellement des ressources doivent devenir des ressources distinctes avec des expressions `when:`.

## Conditionnels et ciblage

Conditionnels Salt utilisant Jinja2 dans les fichiers d'état :

```yaml
{% if grains['os_family'] == 'Debian' %}
nginx-package:
  pkg.installed:
    - name: nginx
{% endif %}
```

Équivalent Vigo utilisant `when:` :

```yaml
- name: nginx-package
  type: package
  package: nginx
  state: present
  when: "os_family('debian')"
```

Salt peut aussi utiliser `onlyif`/`unless` sur les appels d'état. Le `when:` de Vigo couvre à la fois les conditionnels structurels et les gardes à l'exécution.

## Secrets

Salt utilise des données pillar chiffrées par GPG ou des modules pillar externes :

```yaml
# pillar/secrets.sls (GPG encrypted)
db_password: |
  -----BEGIN PGP MESSAGE-----
  ...
  -----END PGP MESSAGE-----
```

Vigo utilise le préfixe `secret:` :

```yaml
content: "secret:vigo/database/password"
```

Les secrets sont résolus côté serveur au moment de la compilation du bundle. Pas de gestion de clés GPG sur les minions.

## Pièges courants

1. **Pas de Jinja2 dans la structure YAML.** C'est l'ajustement n°1. Les états Salt utilisent couramment `{% if %}` pour inclure conditionnellement des blocs de ressources entiers. Dans Vigo, chaque ressource est toujours présente dans le YAML — utilisez `when:` pour ignorer conditionnellement l'exécution.

2. **Pas de modules d'exécution.** Le `salt['cmd.run']()` ou `salt['file.read']()` de Salt dans Jinja2 n'a pas d'équivalent. Vigo n'exécute pas de code arbitraire pendant la compilation de la config. Utilisez les traits pour les faits système, le `common.vgo` au niveau du répertoire pour les données partagées, ou pré-remplissez les vars.


3. **Pas de fusion de pillar.** Salt peut fusionner les données pillar de plusieurs sources et environnements. Vigo utilise une simple surcharge à trois niveaux : valeurs par défaut du configcrate → vars de rôle → vars de nœud. Le dernier écrivain l'emporte à chaque niveau.

4. **Pas d'URI de fichiers `salt://`.** Salt sert les fichiers depuis les file_roots via `salt://`. Vigo intègre les modèles dans `content:` ou utilise `sourcepath:` pour les fichiers locaux à l'agent. Il n'y a pas de serveur de fichiers.

5. **Modèles uniquement dans `content:`.** Salt effectue le rendu Jinja2 dans n'importe quelle valeur YAML. Vigo restreint les modèles Go à `content:`. N'essayez pas de templater `package:`, `target_path:` ou `command:`.

6. **Pas de ciblage composé.** Les matchers composés de Salt (`G@os:CentOS and P@hostname:web.*`) n'ont pas d'équivalent direct. Utilisez des motifs glob dans `envoys.vgo` et `when:` sur des ressources individuelles pour un ciblage fin.

7. **Pas de bus d'événements ni de reactor.** Le système reactor piloté par événements de Salt n'existe pas dans Vigo. Pour les flux pilotés par événements, utilisez les webhooks ou le système de tâches.

8. **Enrôlement de l'agent requis.** Les minions Salt s'enregistrent automatiquement via l'acceptation de clé. Vigo requiert un enrôlement explicite via un jeton à usage unique généré avec `vigocli tokens generate`.

## Liens connexes

- [Format de configuration](../../reference/configcrate-language.md) — Configcrates, rôles, envoys.vgo, vars
- [DAG de ressources](../../reference/configcrate-language.md) — depends_on, notify, subscribes
- [Modèles](../../reference/configcrate-language.md) — Syntaxe et restrictions des modèles Go
- [Expressions When](../../reference/configcrate-language.md) — Expressions conditionnelles et fonctions intégrées
- [Secrets](../../reference/server-yaml.md) — Fournisseurs de secrets et le préfixe secret:
- [Exécuteurs](../../reference/vigo/resources/_index.md) — Tous les exécuteurs de ressources
- [Exemple de configcrate Nginx](../../examples/nginx-module.md) — Tutoriel nginx complet
- [Premier configcrate](../../howto/configcrates/write-your-first-configcrate.md) — Écrire votre premier configcrate

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*

*Confidentiel — Alexander4, LLC. Ne pas redistribuer. Voir [../legal/license.md](../../legal/license.md).*
