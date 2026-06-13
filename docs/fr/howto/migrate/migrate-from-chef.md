<!-- vigo-i18n source_sha:8b5fdbae155970a9c2c5bbbb3fa7f515db6b9a589fed4e3b20cdea92216a8959 -->

# Migration depuis Chef

Chef et Vigo partagent le modèle pull et le motif de convergence à intervalle régulier. Les principaux changements pratiques : DSL Ruby → YAML simple ; les 15 niveaux de précédence d'attributs de Chef → les trois niveaux de Vigo (vars de bloc match > vars héritées > valeurs par défaut du configcrate, avec les surcharges `environments.vgo` au sommet). La forme des ressources — déclarative, idempotente, avec les déclencheurs `notify` / `subscribes` — se traduit étroitement.

## Différences d'architecture

Chef comme Vigo utilisent un agent pull qui contacte un serveur à intervalle régulier, reçoit l'état désiré et converge localement. Chef compile du code Ruby en une collection de ressources ; Vigo interprète le YAML directement — il n'y a pas d'étape de compilation.

L'infrastructure de Chef comprend un Chef Server (ou Chef Automate), des nœuds clients exécutant chef-client (Ruby), et souvent un Supermarket pour la distribution des cookbooks. Vigo dispose d'un serveur Go (vigosrv), d'un agent Rust (vigo) et d'une CLI (vigocli). Aucun runtime Ruby, aucun gestionnaire de paquets, aucun service externe.

Chef stocke les objets de nœud et les index de recherche sur le serveur (PostgreSQL + Elasticsearch dans Chef Server). Vigo utilise un FleetIndex en mémoire adossé à SQLite — aucune base de données externe.

Le système d'attributs de Chef compte 15 niveaux de précédence (default, force_default, normal, override, force_override, automatic — chacun aux niveaux role, environment, cookbook et recipe). Vigo a trois niveaux : valeurs par défaut du configcrate, vars de role, vars de nœud.

## Correspondance des concepts

| Concept Chef | Équivalent Vigo | Notes |
|---|---|---|
| Cookbook | Configcrate (fichier `.vgo`) | Un seul fichier YAML remplace l'arborescence du cookbook |
| Recipe | Ressources du configcrate | Ressources listées dans le configcrate — pas de fichiers recipe séparés |
| Role | Role | Liste nommée de configcrates — même concept |
| Run list | Liste de configcrates dans `envoys.vgo` | Roles et configcrates assignés aux Envoys correspondants |
| Node object | Entrée FleetIndex + Traits | En mémoire, non stocké côté serveur comme un document |
| Ohai | Traits | Faits système collectés par l'agent |
| Attributes (default) | `vars:` du configcrate | Valeurs par défaut dans le configcrate |
| Attributes (override) | `vars:` de bloc match dans `envoys.vgo` | Surcharges au niveau match |
| Data bag | `common.vgo` à n'importe quel niveau de répertoire | Les vars partagées héritées par répertoire remplacent les fichiers de données YAML externes |
| Chef Vault | Préfixe `secret:` | Résolu côté serveur, jamais en clair |
| Environment | `environments.vgo` (indexé par `environment:`) | Les surcharges propres à un environnement vivent dans un fichier voisin, pas par bloc match |
| `notifies` | `notify` | Déclenche une ressource en aval lors d'un changement |
| `subscribes` | `subscribes` | Même mot-clé, même sémantique |
| `only_if` / `not_if` | `when:` | Conditionnels de ressource |
| `include_recipe` | Dépendances de configcrate | Configcrates listés dans les roles ou `depends_on` |
| `knife` | `vigocli` | CLI pour l'administration du serveur |
| Chef Supermarket | Aucun équivalent | Les configcrates sont de simples fichiers YAML — pas de registre |
| Policyfile | `envoys.vgo` | Épingle les versions de configcrate au niveau du nœud |
| `chef-client` | Agent `vigo` | Binaire Rust, état LMDB, convergence hors ligne |
| `chef-server` | `vigosrv` | Binaire Go, gRPC + REST + Web UI |
| Template ERB | Template Go dans `content:` | Templates uniquement dans l'attribut `content:` |
| `knife bootstrap` | `vigocli tokens` + script bootstrap | Enrôlement par jeton à usage unique |

## Correspondance des types de ressources

| Ressource Chef | Exécuteur Vigo | Notes |
|---|---|---|
| `package` | `package` | Détecte automatiquement le gestionnaire de paquets |
| `template` / `cookbook_file` | `file` | `content:` pour les templates, `sourcepath:` pour les fichiers statiques |
| `service` | `service` | `state: running/stopped`, `enabled: true/false` |
| `execute` / `bash` | `exec` | `command:`, `onlyif:`, `unless:` |
| `user` | `user` | `username:`, `uid:`, `shell:`, `groups:` |
| `group` | `group` | `groupname:`, `gid:`, `members:` |
| `cron` | `cron` | `command:`, `minute:`, `hour:`, etc. |
| `apt_repository` / `yum_repository` | `repository` | Unifié entre distributions |
| `remote_file` (archive) | `source_package` | `url:`, `target_path:`, `checksum:` (tarballs/zips) |
| `remote_file` + `dpkg_package`/`rpm_package` | `nonrepo_package` | Installe .deb/.rpm/.pkg/.msi depuis des URLs |
| `mount` | `mount` | `device:`, `mountpoint:`, `fstype:` |
| `sysctl` | `sysctl` | `key:`, `value:` |
| `hostname` | `hostname` | `hostname:` |
| `file` (line) | `file_line` | `path:`, `line:`, `match:` |


## Tutoriel : conversion d'un cookbook Nginx

### Version Chef

`cookbooks/nginx/recipes/default.rb` :

```ruby
package 'nginx' do
  action :install
end

template '/etc/nginx/nginx.conf' do
  source 'nginx.conf.erb'
  owner 'root'
  group 'root'
  mode '0644'
  variables(
    worker_processes: node['nginx']['worker_processes'],
    worker_connections: node['nginx']['worker_connections'],
    server_name: node['nginx']['server_name'],
    root: node['nginx']['root']
  )
  notifies :restart, 'service[nginx]'
end

service 'nginx' do
  action [:enable, :start]
end
```

`cookbooks/nginx/attributes/default.rb` :

```ruby
default['nginx']['worker_processes'] = 'auto'
default['nginx']['worker_connections'] = 1024
default['nginx']['server_name'] = 'localhost'
default['nginx']['root'] = '/var/www/html'
```

`cookbooks/nginx/templates/default/nginx.conf.erb` :

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

- **Pas de Ruby.** Toute la pile de Chef tourne sur Ruby — cookbooks, recipes, attributes, templates. Vigo est du YAML pur avec des templates Go dans `content:` uniquement.
- **Un fichier par configcrate.** Un cookbook Chef est une arborescence de répertoires (`recipes/`, `templates/`, `attributes/`, `metadata.rb`, etc.). Un configcrate Vigo est un unique fichier `.vgo`.
- **Pas de niveaux d'attributs.** Les niveaux `default`/`normal`/`override`/`force_override`/`automatic` de Chef aux niveaux cookbook/role/environment/node donnent 15 paliers de précédence. Vigo en a trois : valeurs par défaut du configcrate → vars de role → vars de nœud.
- **Templates en ligne ou dans `templates/`.** Les petits templates vont directement dans `content:`. Pour les fichiers plus volumineux, utilisez `source: templates/nginx.conf.tmpl` — le serveur lit le fichier depuis le répertoire `templates/` et le rend avec le même moteur de templates Go. Pas de convention de sous-répertoire `templates/default/`.

## Hiérarchie des variables et des données

Les attributs Chef ont 15 niveaux de précédence. En pratique, la plupart des équipes utilisent `default` et `override`. Vigo établit une correspondance nette :

| Niveau Chef | Équivalent Vigo |
|---|---|
| `default` (cookbook) | `vars:` du configcrate |
| `default` (role) | `vars:` du role |
| `default` (environment) | vars de `common.vgo` dans les répertoires ancêtres |
| `override` (node) | `vars:` de bloc match |
| `override` (environment) | `environments.vgo` indexé par `environment:` |
| `automatic` (Ohai) | Traits (accessibles via `.Traits` dans les templates, ou `when:`) |
| Data bags | `common.vgo` à n'importe quel niveau de répertoire (partagé par héritage) |

Les surcharges par environnement vivent dans `environments.vgo`, pas par bloc match :

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

Chef utilise ERB. Vigo utilise les templates Go.

| ERB Chef | Template Go Vigo |
|---|---|
| `<%= @variable %>` | `{{ .Vars.variable }}` |
| `<%= node['attr'] %>` | `{{ .Vars.attr }}` |

| `<%= node['fqdn'] %>` | `{{ .Traits.identity.fqdn }}` |
| `<% if condition %>` | `{{ if .Vars.condition }}` |
| `<% @items.each do \|i\| %>` | `{{ range .Vars.items }}` |

**Restriction critique :** les templates Go ne fonctionnent que dans les attributs `content:`. Vous ne pouvez pas utiliser `{{ }}` dans `package:`, `target_path:`, `command:`, ou tout autre attribut.

## Conditionnels et ciblage

Conditionnels Chef :

```ruby
package 'nginx' do
  action :install
  only_if { node['platform_family'] == 'debian' }
end
```

Équivalent Vigo :

```yaml
- name: nginx-package
  type: package
  package: nginx
  state: present
  when: "os_family('debian')"
```

Les gardes `only_if`/`not_if` de Chef exécutent du Ruby arbitraire. Le `when:` de Vigo utilise une syntaxe d'expression fixe avec 15 fonctions intégrées et des opérateurs booléens.

## Secrets

Chef utilise des data bags (éventuellement chiffrés) ou Chef Vault :

```ruby
db_password = data_bag_item('secrets', 'database')['password']
```

Vigo utilise le préfixe `secret:` :

```yaml
content: "secret:vigo/database/password"
```

Le fournisseur de secrets résout les références au moment de la compilation du bundle. Aucun code Ruby, aucune cérémonie de gestion de clés.

## Pièges courants

1. **Pas de Ruby.** Vous ne pouvez pas exécuter de code arbitraire pendant la convergence. Si les recipes Chef contiennent une logique Ruby procédurale au-delà des déclarations de ressources, vous devrez la décomposer en ressources déclaratives avec des conditionnels `when:`.

2. **Pas de phase de compilation.** Chef a un modèle en deux phases (compile puis converge). Vigo n'a pas de phase de compilation — les ressources sont interprétées directement depuis le YAML. Il n'y a pas d'équivalent à l'évaluation `lazy {}`.

3. **Templates uniquement dans `content:`.** Chef vous laisse utiliser ERB dans n'importe quelle chaîne et des méthodes d'aide aux templates partout. Vigo restreint les templates Go à `content:`. Utilisez `when:` et des ressources séparées pour la logique conditionnelle.

4. **Espace de noms de variables plat.** Les attributs Chef sont des hachages imbriqués (`node['nginx']['port']`). Les vars Vigo sont des chaînes plates (`nginx_port`). Préfixez avec le nom du configcrate pour éviter les collisions.

5. **Pas de recherche.** L'API de recherche de Chef Server (`search(:node, 'role:webserver')`) n'a pas d'équivalent direct. Pour la coordination inter-nœuds, utilisez l'héritage `common.vgo` au niveau des répertoires ou le système de tâches/requêtes.

6. **Pas de manipulation de `run_list`.** Chef peut inclure des recipes dynamiquement. La liste de configcrates de Vigo est statique — définie dans les assignations de role de `envoys.vgo`. Utilisez `when:` pour ignorer conditionnellement des configcrates entiers.

7. **Pas de résolution de dépendances Berkshelf/Policyfile.** Les configcrates ne déclarent pas de dépendances envers d'autres configcrates. Incluez tous les configcrates requis dans la définition du role.

8. **Classification de nœud par premier match gagnant.** Chef peut fusionner plusieurs roles et environments. Le `envoys.vgo` de Vigo utilise le premier match gagnant — seul le premier glob correspondant s'applique.

## Voir aussi

- [Format de configuration](../../reference/configcrate-language.md) — Configcrates, roles, envoys.vgo, vars
- [DAG de ressources](../../reference/configcrate-language.md) — depends_on, notify, subscribes
- [Templates](../../reference/configcrate-language.md) — Syntaxe et restrictions des templates Go
- [Expressions When](../../reference/configcrate-language.md) — Expressions conditionnelles et fonctions intégrées
- [Secrets](../../reference/server-yaml.md) — Fournisseurs de secrets et le préfixe secret:
- [Exécuteurs](../../reference/vigo/resources/_index.md) — Tous les exécuteurs de ressources
- [Exemple de Configcrate Nginx](../../examples/nginx-module.md) — Tutoriel nginx complet
- [Premier Configcrate](../../howto/configcrates/write-your-first-configcrate.md) — Écrire votre premier configcrate

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*

*Confidentiel — Alexander4, LLC. Ne pas redistribuer. Voir [../legal/license.md](../../legal/license.md).*
