<!-- vigo-i18n source_sha:8e08f8cd9a84ff71c66a423c16e45e4f114a4510772973d1e7d1ac427998465c -->

# Migrer depuis CFEngine

CFEngine et Vigo modélisent tous deux l'état désiré comme des unités déclaratives que les agents évaluent et convergent localement. Le modèle de ressources de Vigo correspond aux promesses CFEngine, les configcrates aux bundles, et les expressions `when:` au système de classes. Le changement est surtout notationnel (langage de politique CFEngine → YAML) auquel s'ajoute un modèle d'identité différent côté serveur (hostcrates + rôles + environnements).

## Différences d'architecture

CFEngine comme Vigo utilisent un agent en mode pull qui récupère la politique depuis un serveur et converge localement. Le cf-agent de CFEngine évalue des promesses écrites dans le DSL CFEngine ; l'agent de Vigo interprète des ressources YAML compilées en bundles signés par le serveur.

CFEngine comporte plusieurs démons coopérants (cf-serverd, cf-execd, cf-monitord, cf-hub). Vigo consolide tout dans un binaire serveur unique (vigosrv) qui gère gRPC, REST, les métriques et l'interface web.

La force de CFEngine réside dans son agent C léger avec un cycle de convergence de 5 minutes. L'agent de Vigo est en Rust (binaire statique unique) avec un intervalle de check-in configurable et des bundles de promesses compilés qui permettent la convergence hors ligne — l'agent met en cache son dernier bundle signé et continue de l'appliquer lorsque le serveur est injoignable.

La force de CFEngine est son système de classes, un puissant mécanisme de marquage booléen pour les politiques conditionnelles. Vigo le remplace par les expressions `when:` — une syntaxe plus simple avec 15 fonctions intégrées qui couvre la plupart des motifs basés sur les classes sans la complexité de l'arithmétique de classes.

## Correspondance des concepts

| Concept CFEngine | Équivalent Vigo | Remarques |
|---|---|---|
| Promise | Resource | Unité déclarative d'état désiré |
| Bundle | Configcrate (fichier `.vgo`) | Collection nommée de ressources |
| Body | Valeurs par défaut / attributs | Valeurs d'attributs au niveau de la ressource |
| Namespace | Préfixe de nom de configcrate | Espace de noms plat — préfixer pour éviter les collisions |
| `promises.cf` / `update.cf` | `envoys.vgo` | Configuration du hub de politique → classification des nœuds |
| Classe dure (p. ex. `debian`) | `when:` basé sur les traits | `when: "os_family('debian')"` |
| Classe douce (définie) | `when:` avec `has_trait()` | Conditions booléennes sur l'état du système |
| Expression de classe (`debian.webserver`) | `when:` avec `&&` | `when: "os_family('debian') && has_trait('role', 'web')"` |
| `cf-serverd` | `vigosrv` | Binaire Go, gRPC + REST + interface web |
| `cf-agent` | agent `vigo` | Binaire Rust, magasin d'état LMDB |
| `cf-execd` | Service systemd de l'agent | L'agent s'exécute en démon avec planification intégrée |
| `cf-monitord` | Traits | Surveillance côté agent remplacée par la collecte de traits |
| `cf-hub` | FleetIndex + SQLite | Agrégation centrale des données |
| Masterfiles | `stacks/` | Dépôt de politique |
| Augments (`def.json`) | `vars` / `common.vgo` / `environments.vgo` | Données de variables externes, en couches par héritage |
| Protocole de module | Exécuteurs personnalisés | Protocole JSON stdin/stdout pour les extensions |
| `cf-secret` | Préfixe `secret:` | Secrets résolus à la compilation du bundle |
| Template Mustache | Template Go dans `content:` | Templates uniquement dans l'attribut `content:` |
| `edit_line` | `file_line` / `blockinfile` / `stream_edit:` | Plusieurs options pour l'édition en place |
| Type de promesse `services:` | Exécuteur `service` | `state: running/stopped`, `enabled: true/false` |
| Type de promesse `reports:` | Aucun équivalent | Utiliser le journal d'audit ou les résultats de tâches pour le reporting |
| Type de promesse `meta:` | Aucun équivalent | Aucun système de métadonnées de politique |

## Correspondance des types de ressources

| Type de promesse CFEngine | Exécuteur Vigo | Remarques |
|---|---|---|
| `packages:` (policy) | `package` | Détecte automatiquement le gestionnaire de paquets |
| `files:` (copy) | `file` avec `sourcepath:` | Copie depuis un chemin local à l'agent |
| `files:` (content) | `file` avec `content:` | Template en ligne |
| `files:` (`edit_line`) | `file_line` / `blockinfile` | Édition de ligne ou de bloc |
| `services:` | `service` | `state: running/stopped`, `enabled: true/false` |
| `commands:` | `exec` | `command:`, `onlyif:`, `unless:` |
| `users:` | `user` | `username:`, `uid:`, `shell:`, `groups:` |
| `storage:` (mount) | `mount` | `device:`, `mountpoint:`, `fstype:` |
| `files:` (link) | `symlink` | `target_path:`, `source:` |
| `files:` (perms) | `file` | `owner:`, `group:`, `mode:` |
| `vars:` (sysctl) | `sysctl` | `key:`, `value:` |
| `files:` (insertion `edit_line`) | `file_line` | `path:`, `line:`, `match:` |

## Pas à pas : conversion d'un bundle Nginx


### Version CFEngine

```cfengine3
bundle agent nginx
{
  vars:
    "worker_processes"   string => "auto";
    "worker_connections" string => "1024";
    "server_name"        string => "localhost";
    "root"               string => "/var/www/html";

  packages:
    "nginx"
      policy => "present",
      package_module => apt;

  files:
    "/etc/nginx/nginx.conf"
      content => mustache("nginx.conf.mustache", "nginx"),
      perms   => mog("0644", "root", "root"),
      classes => if_repaired("nginx_config_changed");

  services:
    "nginx"
      service_policy => "start";

    nginx_config_changed::
    "nginx"
      service_policy => "restart";
}
```

Avec `nginx.conf.mustache` :

```mustache
worker_processes {{vars.nginx.worker_processes}};
events {
    worker_connections {{vars.nginx.worker_connections}};
}
http {
    include       /etc/nginx/mime.types;
    default_type  application/octet-stream;
    sendfile      on;
    keepalive_timeout 65;

    server {
        listen 80;
        server_name {{vars.nginx.server_name}};
        root {{vars.nginx.root}};

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

- **Pas de redémarrage basé sur les classes.** CFEngine utilise `classes => if_repaired("nginx_config_changed")` pour définir une classe, puis redémarre conditionnellement dans une promesse `services:` distincte protégée par `nginx_config_changed::`. Vigo utilise `notify: [nginx-service]` directement — lorsque le fichier change, le service redémarre.
- **YAML plutôt que DSL.** La syntaxe de promesse de CFEngine impose d'apprendre son DSL, ses bodies et ses types de promesses. Vigo est du YAML brut avec un petit ensemble de clés connues.
- **Templates en ligne ou dans `templates/`.** Les petits templates vont dans `content:` en syntaxe de template Go. Les fichiers plus volumineux utilisent `source: templates/app.conf.tmpl` — le serveur lit et rend le fichier depuis le répertoire `templates/`.
- **Accès aux variables simplifié.** Le `{{vars.bundle.varname}}` de CFEngine devient `{{ .Vars.varname }}` dans Vigo.

## Hiérarchie des variables et des données

Les variables CFEngine vivent dans la portée du bundle et peuvent être définies via les augments (`def.json`). La hiérarchie de Vigo :

| Niveau CFEngine | Équivalent Vigo |
|---|---|
| `vars:` du bundle | `vars:` du configcrate (valeurs par défaut) |
| Augments (`def.json`) | Vars `common.vgo` dans les répertoires ancêtres (héritées) |
| Classes dures | Traits (disponibles dans `when:` et les templates) |
| `host_specific_data/` | `vars:` du bloc match dans `envoys.vgo` |
| Environnements | `environments.vgo` indexé par `environment:` |

Les surcharges par environnement vivent dans un fichier voisin, et non en ligne sur chaque bloc match :

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

## Templates

CFEngine utilise les templates Mustache. Vigo utilise les templates Go.

| Mustache CFEngine | Template Go Vigo |
|---|---|
| `{{vars.bundle.variable}}` | `{{ .Vars.variable }}` |
| `{{classes.classname}}` | `{{ if .Traits.os.family }}` ou `when:` |
| `{{#classes.debian}}...{{/classes.debian}}` | `{{ if eq .Traits.os.family "debian" }}...{{ end }}` |
| `{{%vars.bundle.variable}}` (itération) | `{{ range .Vars.list }}` |

**Restriction critique :** les templates ne fonctionnent que dans les attributs `content:`. Tous les autres attributs de ressource sont des valeurs YAML littérales.

## Conditionnels et ciblage


Le système de classes de CFEngine :

```cfengine3
debian::
  packages:
    "nginx"
      policy => "present";

redhat::
  packages:
    "nginx"
      policy => "present";
```

Équivalent Vigo avec `when:` :

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

Expressions de classe CFEngine :

| Expression de classe CFEngine | `when:` Vigo |
|---|---|
| `debian` | `os_family('debian')` |
| `debian.webserver` | `os_family('debian') && has_trait('role', 'web')` |
| `!windows` | `!os_family('windows')` |
| `debian\|ubuntu` | `os_family('debian')` (couvre les deux) |
| `any` | (omettre `when:` — s'applique toujours) |

## Secrets

CFEngine utilise `cf-secret` pour chiffrer les données :

```bash
cf-secret encrypt -o secret.dat -k /path/to/pubkey plaintext.txt
```

Vigo utilise le préfixe `secret:` en ligne :

```yaml
content: "secret:vigo/database/password"
```

Le fournisseur de secrets résout les références à la compilation du bundle. Aucune gestion de clés sur les agents.

## Pièges courants

1. **Pas de système de classes.** Les classes de CFEngine forment un espace de noms booléen global évalué à chaque promesse. Les expressions `when:` de Vigo sont par ressource et ne se propagent pas. Il n'y a pas d'équivalent au fait de définir une classe dans une ressource et de la lire dans une autre — utilisez `notify`/`subscribes` pour les déclencheurs inter-ressources.


2. **Pas de verrous de convergence.** Le `action => if_elapsed("60")` de CFEngine limite la fréquence d'évaluation des promesses. Vigo évalue toutes les ressources à chaque check-in. Utilisez l'intervalle de check-in pour contrôler la fréquence de convergence.

3. **Pas d'analyse de sortie du protocole de module.** Les modules CFEngine impriment `+classname` et `=variable=value` sur stdout pour l'intégration. Les exécuteurs personnalisés de Vigo utilisent un protocole JSON stdin/stdout — format différent, même objectif.

4. **Templates uniquement dans `content:`.** CFEngine autorise Mustache dans toute promesse de fichier. Vigo restreint les templates Go à `content:`. Utilisez `when:` pour la logique conditionnelle dans les attributs de ressource.

5. **Espace de noms de variables plat.** CFEngine cantonne les variables aux bundles. Les vars de Vigo sont des chaînes plates — préfixez avec le nom du configcrate pour éviter les collisions.

6. **Pas de `meta:` ni de `reports:`.** Les types de promesses de métadonnées et de reporting de CFEngine n'existent pas. Utilisez le journal d'audit, les webhooks ou l'API REST pour l'observabilité.

7. **Classification au premier match gagnant.** La politique basée sur les classes de CFEngine peut appliquer plusieurs bundles selon plusieurs classes correspondantes. Le `envoys.vgo` de Vigo applique le premier match gagnant — seul le premier glob correspondant s'applique. Ordonnez les entrées du plus spécifique au moins spécifique.

8. **Pas d'édition convergente `edit_line`.** Le body `edit_line` de CFEngine, avec insertion/suppression convergente, est puissant. Vigo propose `file_line` (lignes uniques), `blockinfile` (blocs) et `stream_edit:` (passage par des scripts externes). Pour les transformations de fichiers complexes, `stream_edit:` est l'équivalent le plus proche.

## Voir aussi

- [Format de configuration](../../reference/configcrate-language.md) — Configcrates, rôles, envoys.vgo, vars
- [DAG des ressources](../../reference/configcrate-language.md) — depends_on, notify, subscribes
- [Templates](../../reference/configcrate-language.md) — Syntaxe et restrictions des templates Go
- [Expressions When](../../reference/configcrate-language.md) — Expressions conditionnelles et fonctions intégrées
- [Secrets](../../reference/server-yaml.md) — Fournisseurs de secrets et le préfixe secret:
- [Exécuteurs personnalisés](../../reference/configcrate-language.md) — Protocole JSON pour les extensions
- [Exécuteurs](../../reference/vigo/resources/_index.md) — Tous les exécuteurs de ressources
- [Exemple de configcrate Nginx](../../examples/nginx-module.md) — Pas à pas complet de nginx
- [Premier configcrate](../../howto/configcrates/write-your-first-configcrate.md) — Écrire votre premier configcrate

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*

*Confidentiel — Alexander4, LLC. Ne pas redistribuer. Voir [../legal/license.md](../../legal/license.md).*
