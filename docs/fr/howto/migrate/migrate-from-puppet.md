<!-- vigo-i18n source_sha:70c816c2a283f8d53dd9846137ce8ba6092a9767911c190fa630fb19ea413038 -->

# Migrer depuis Puppet

Puppet et Vigo partagent la forme conceptuelle la plus proche — les deux fonctionnent en mode pull, les deux compilent un catalogue d'état désiré sur le serveur, et les deux appliquent des ressources idempotentes sur l'agent. La migration consiste essentiellement à traduire le DSL Puppet en YAML et Hiera vers la précédence de variables à trois niveaux de Vigo ; le modèle opérationnel est identique.

## Différences d'architecture

Puppet et Vigo suivent tous deux un modèle pull : un agent contacte un serveur central, reçoit l'état désiré et converge localement. Le serveur compile un catalogue (Puppet) ou un bundle (Vigo) en fonction de l'identité et des facts/traits du nœud.

Puppet utilise son propre DSL pour les manifestes et ERB/EPP pour les templates. Vigo utilise du YAML simple pour tout et des templates Go uniquement pour les attributs `content:`. Il n'y a pas d'étape de compilation — le YAML est la configuration.

Puppet stocke l'état du catalogue dans PuppetDB et l'interroge pour les ressources exportées et le reporting. Vigo conserve un FleetIndex en mémoire (réhydraté depuis SQLite au démarrage) et ne touche jamais à la base de données sur le chemin critique du check-in.

L'agent Puppet s'exécute par défaut à intervalle de 30 minutes. L'intervalle de l'agent Vigo est configurable et prend en charge les promesses compilées avec convergence hors ligne — l'agent met en cache son dernier bundle et continue de l'appliquer lorsque le serveur est injoignable.

## Correspondance des concepts

| Concept Puppet | Équivalent Vigo | Notes |
|---|---|---|
| Manifeste (fichier `.pp`) | Configcrate (fichier `.vgo`) | YAML au lieu du DSL Puppet |
| Classe / Type défini | Configcrate | Un configcrate = un fichier `.vgo` avec des ressources |
| Profile | Configcrate | Même niveau d'abstraction |
| Role | Role | Liste nommée de configcrates |
| `site.pp` / ENC | `envoys.vgo` | Motifs glob, premier-match-gagnant |
| Hiera | `vars` / `common.vgo` / `environments.vgo` | 4 couches : valeurs par défaut du configcrate → common.vgo hérité → bloc match → par environnement |
| Fact Facter | Trait | Collecté automatiquement, disponible dans `when:` et les templates |
| Catalog | Bundle | État désiré compilé par le serveur et envoyé à l'agent |
| Resource | Resource | Même concept — idempotente, déclarative |
| `require` / `before` | `depends_on` / `before` | Ordonnancement DAG |
| `notify` | `notify` | Déclenche la réapplication d'une ressource en aval |
| `subscribe` | `subscribes` | Déclencheur en mode pull |
| `ensure => absent` | `state: absent` | Mêmes sémantiques |
| Module Puppet Forge | Fichier configcrate `.vgo` | Pas de gestionnaire de paquets — juste des fichiers YAML |
| PuppetDB | FleetIndex + SQLite | Index en mémoire, pas de base de données externe |
| hiera-eyaml | Préfixe `secret:` | Résolu à la compilation du bundle |
| Template ERB | Template Go dans `content:` | Templates uniquement dans `content:` — jamais dans `target_path:`, `package:`, etc. |
| `environments/` | `environments.vgo` (indexé par `environment:` du bloc match) | Surcharges spécifiques à l'environnement, non basées sur les répertoires |
| Agent Puppet | agent vigo | Binaire Rust, magasin d'état LMDB |
| Serveur Puppet | vigosrv | Binaire Go, gRPC + REST |
| `puppet cert` | `vigocli tokens` | Enrôlement par jeton à usage unique au lieu de la signature de certificat |

## Correspondance des types de ressources

| Ressource Puppet | Exécuteur Vigo | Notes |
|---|---|---|
| `package` | `package` | Détecte automatiquement le gestionnaire de paquets |
| `file` | `file` | Templates Go dans `content:`, ou `sourcepath:` pour les copies |
| `service` | `service` | `state: running/stopped`, `enabled: true/false` |
| `exec` | `exec` | `command:`, `onlyif:`, `unless:` |
| `user` | `user` | `username:`, `uid:`, `shell:`, `groups:` |
| `group` | `group` | `groupname:`, `gid:`, `members:` |
| `cron` | `cron` | `command:`, `minute:`, `hour:`, etc. |
| `file_line` | `file_line` | `path:`, `line:`, `match:` |
| `yumrepo` / `apt::source` | `repository` | Unifié entre distributions |
| `archive` | `source_package` | `url:`, `target_path:`, `checksum:` (tarballs/zips) |
| `package` (avec `source`) | `nonrepo_package` | Installe des .deb/.rpm/.pkg/.msi depuis des URL |
| `mount` | `mount` | `device:`, `mountpoint:`, `fstype:` |
| `sysctl` | `sysctl` | `key:`, `value:` |
| `host` | `host` | `hostname:`, `ip:` |

## Tutoriel : convertir un configcrate Nginx


### Version Puppet

```puppet
class nginx (
  String $worker_processes   = 'auto',
  Integer $worker_connections = 1024,
  String $server_name        = 'localhost',
  String $root               = '/var/www/html',
) {
  package { 'nginx':
    ensure => installed,
  }

  file { '/etc/nginx/nginx.conf':
    ensure  => file,
    owner   => 'root',
    group   => 'root',
    mode    => '0644',
    content => template('nginx/nginx.conf.erb'),
    notify  => Service['nginx'],
    require => Package['nginx'],
  }

  service { 'nginx':
    ensure => running,
    enable => true,
  }
}
```

Avec `templates/nginx.conf.erb` :

```erb
worker_processes <%= @worker_processes %>;
events {
    worker_connections <%= @worker_connections %>;
}
http {
    include       /etc/nginx/mime.types;
    default_type  application/octet-stream;
    sendfile      on;
    keepalive_timeout 65;

    server {
        listen 80;
        server_name <%= @server_name %>;
        root <%= @root %>;

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

- **Templates inline ou dans `templates/`.** Les petits templates vont inline dans `content:`. Les fichiers plus volumineux utilisent `source: templates/nginx.conf.tmpl` — le serveur lit et rend le fichier depuis le répertoire `templates/`. Pas d'ERB — uniquement la syntaxe des templates Go.
- **Pas de `require =>`** nécessaire pour l'ordonnancement paquet → config. Vigo utilise `notify:` de la config vers le service, et le DAG de ressources gère le reste. Vous pouvez ajouter un `depends_on: [nginx-package]` explicite si vous préférez.
- **Les variables sont plates.** Les paramètres de classe Puppet deviennent des entrées `vars:`. Pas de hiérarchie de portée — valeurs par défaut du configcrate, puis surcharges au niveau de l'envoy. C'est tout.
- **Pas de compilation de catalogue.** Le YAML est interprété directement. Pas de parseur, pas d'AST, pas d'erreurs de compilation à déboguer.

## Hiérarchie des variables et des données

Hiera de Puppet fournit jusqu'à plus de 10 niveaux de hiérarchie (common.yaml → famille d'OS → environnement → datacenter → FQDN). Vigo en a délibérément quatre :

1. **Valeurs par défaut du configcrate** — `vars:` dans le fichier `.vgo` du configcrate
2. **Variables `common.vgo` héritées** — définies dans n'importe quel répertoire ancêtre, cascadent vers les sous-répertoires
3. **Variables du bloc match** — `vars:` sur l'entrée `envoys.vgo`
4. **Variables `environments.vgo`** — sélectionnées par la valeur `environment:` du bloc match

Les couches ultérieures surchargent les précédentes. Les valeurs spécifiques à un environnement vivent dans `environments.vgo`, et non sur les blocs match individuels :

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

Pour les valeurs partagées entre de nombreux blocs match, placez-les dans `common.vgo` au répertoire parent approprié — l'héritage de répertoire remplace la recherche hiérarchique Hiera de Puppet.

## Templates

Puppet utilise ERB (ou EPP). Vigo utilise les templates Go. Différences de syntaxe clés :

| ERB Puppet | Template Go Vigo |
|---|---|
| `<%= @variable %>` | `{{ .Vars.variable }}` |
| `<%= scope['module::param'] %>` | `{{ .Vars.param }}` |
| `<%= @fqdn %>` | `{{ .Traits.identity.fqdn }}` |
| `<% if @condition %>` | `{{ if .Vars.condition }}` |
| `<% @items.each do |item| %>` | `{{ range .Vars.items }}` |
| `<%= @variable.downcase %>` | `{{ lower .Vars.variable }}` |

**Restriction critique :** les templates ne sont autorisés que dans les attributs `content:`. Vous ne pouvez pas appliquer de template à `target_path:`, `package:`, `service:` ou tout autre attribut. Utilisez des expressions `when:` pour la logique conditionnelle et des ressources séparées pour les paquets spécifiques à un OS.

## Conditionnels et ciblage

Conditionnels Puppet :


```puppet
if $facts['os']['family'] == 'Debian' {
  package { 'nginx': ensure => installed }
} elsif $facts['os']['family'] == 'RedHat' {
  package { 'nginx': ensure => installed, name => 'nginx' }
}
```

Équivalent Vigo avec `when:` :

```yaml
resources:
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

Les expressions `when:` de Vigo prennent en charge 15 fonctions intégrées (`os_family()`, `is_container`, `has_trait()`, `trait_value()`, etc.) avec des opérateurs booléens (`&&`, `||`, `!`).

## Secrets

Puppet utilise hiera-eyaml pour chiffrer les valeurs dans les fichiers YAML Hiera. Vigo utilise le préfixe `secret:` :

```yaml
# Puppet (hiera-eyaml)
nginx::ssl_key: ENC[PKCS7,MIIBe...]

# Vigo
content: "secret:vigo/nginx/ssl_key"
```

Le fournisseur de secrets résout les références `secret:` à la compilation du bundle. Le backend par défaut `local` mappe les chemins vers des fichiers chiffrés en AES-256-GCM. Les secrets n'apparaissent jamais en clair dans les configs, les journaux ou les charges utiles gRPC.

## Pièges courants

1. **Pas d'auto-require.** Puppet ordonne automatiquement `Package` avant `File` avant `Service`. Vigo exige un `depends_on:` explicite ou `notify:`/`subscribes:` pour l'ordonnancement. Sans eux, les ressources peuvent s'exécuter dans n'importe quel ordre.

2. **Templates uniquement dans `content:`.** Puppet vous permet d'appliquer un template à n'importe quelle valeur d'attribut. Vigo restreint les templates Go à l'attribut `content:`. Pour les paquets conditionnels selon l'OS, utilisez `when:` avec des ressources séparées — n'essayez pas d'appliquer un template au champ `package:`.

3. **Pas de ressources exportées.** Les ressources exportées `@@` de Puppet et les collecteurs PuppetDB n'ont pas d'équivalent direct. Partagez les données via l'héritage de `common.vgo` au niveau du répertoire, ou coordonnez via l'orchestration task/workflow.

4. **Espace de noms de variables plat.** Il n'y a pas de portée `$module::param`. Toutes les variables sont des chaînes plates dans un espace de noms unique. Préfixez les noms de variables avec le nom du configcrate (par ex. `nginx_worker_processes`) pour éviter les collisions.

5. **Pas de `ensure => absent` pour tout.** Tous les exécuteurs ne prennent pas en charge `state: absent`. Consultez la documentation de l'exécuteur pour connaître les états disponibles.

6. **Premier-match-gagnant.** La classification de nœuds de Puppet peut fusionner plusieurs entrées correspondantes. Le `envoys.vgo` de Vigo utilise le premier-match-gagnant — seul le premier glob correspondant s'applique. Ordonnez vos entrées du plus spécifique au moins spécifique.

7. **Pas de Puppet Forge.** Il n'y a pas de gestionnaire de paquets pour les configcrates. Les configcrates sont de simples fichiers YAML `.vgo` dans votre répertoire de configuration. Copiez-les, adaptez-les et versionnez-les directement.

8. **Refresh d'exec.** Les ressources `exec` de Puppet utilisent `refreshonly => true` pour ne s'exécuter que sur notification. Vigo utilise `when: "changed"` — la ressource est ignorée lors des check-ins normaux et ne s'exécute que lorsqu'elle est déclenchée par `notify`, `subscribes` ou `watch_secret`.

## Voir aussi

- [Format de configuration](../../reference/configcrate-language.md) — Configcrates, roles, envoys.vgo, vars
- [DAG de ressources](../../reference/configcrate-language.md) — depends_on, notify, subscribes
- [Templates](../../reference/configcrate-language.md) — Syntaxe et restrictions des templates Go
- [Expressions When](../../reference/configcrate-language.md) — Expressions conditionnelles et fonctions intégrées
- [Secrets](../../reference/server-yaml.md) — Fournisseurs de secrets et le préfixe secret:
- [Exécuteurs](../../reference/vigo/resources/_index.md) — Tous les exécuteurs de ressources
- [Exemple de configcrate Nginx](../../examples/nginx-module.md) — Tutoriel nginx complet
- [Premier configcrate](../../howto/configcrates/write-your-first-configcrate.md) — Rédiger votre premier configcrate

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*

*Confidentiel — Alexander4, LLC. Reproduction interdite. Voir [../legal/license.md](../../legal/license.md).*
