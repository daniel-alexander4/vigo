<!-- vigo-i18n source_sha:f8b23408f17bdd8efb6f3cc22915dd703b52a30528a8a6b4f92a7bf67a6f697b -->

# Cluster de base de données

Cet exemple met en place PostgreSQL avec des identifiants gérés via des références `secret:`, un `common.vgo` au niveau du répertoire pour la configuration partagée, et `environments.vgo` pour le réglage spécifique à chaque environnement.

## Configcrate PostgreSQL

`stacks/configcrates/postgresql.vgo` :

```yaml
name: postgresql
vars:
  pg_version: "16"
  pg_port: "5432"
  pg_max_connections: "100"
  pg_shared_buffers: 256MB
  pg_effective_cache_size: 768MB
  pg_work_mem: 4MB
  pg_listen_addresses: localhost
resources:
  - name: pg-package
    type: package
    package: "postgresql-{{ .Vars.pg_version }}"
    state: present
    when: "os_family('debian')"

  - name: pg-package-rhel
    type: package
    package: "postgresql{{ .Vars.pg_version }}-server"
    state: present
    when: "!os_family('debian')"

  - name: pg-config
    type: file
    target_path: "/etc/postgresql/{{ .Vars.pg_version }}/main/postgresql.conf"
    owner: postgres
    group: postgres
    mode: "0644"
    content: |
      port = {{ .Vars.pg_port }}
      max_connections = {{ .Vars.pg_max_connections }}
      shared_buffers = {{ .Vars.pg_shared_buffers }}
      effective_cache_size = {{ .Vars.pg_effective_cache_size }}
      work_mem = {{ .Vars.pg_work_mem }}
      listen_addresses = '{{ .Vars.pg_listen_addresses }}'
      ssl = on
      ssl_cert_file = '/etc/ssl/certs/ssl-cert-snakeoil.pem'
      ssl_key_file = '/etc/ssl/private/ssl-cert-snakeoil.key'
      log_destination = 'syslog'
      logging_collector = off
    notify:
      - pg-service

  - name: pg-hba
    type: file
    target_path: "/etc/postgresql/{{ .Vars.pg_version }}/main/pg_hba.conf"
    owner: postgres
    group: postgres
    mode: "0640"
    content: |
      # TYPE  DATABASE  USER      ADDRESS         METHOD
      local   all       postgres                  peer
      local   all       all                       peer
      host    all       all       127.0.0.1/32    scram-sha-256
      host    all       all       ::1/128         scram-sha-256
      host    all       all       {{ .Vars.pg_allowed_cidr }}  scram-sha-256
    notify:
      - pg-service

  - name: pg-service
    type: service
    service: postgresql
    state: running
    enabled: true
```

## Mise en place de la base de données applicative

`stacks/configcrates/app-database.vgo` :

```yaml
name: app-database
depends_on:
  - postgresql
resources:
  - name: create-app-db
    type: exec
    command: "psql -c \"SELECT 1 FROM pg_database WHERE datname='{{ .Vars.app_db_name }}'\" | grep -q 1 || createdb {{ .Vars.app_db_name }}"
    user: postgres
    unless: "psql -lqt | cut -d \\| -f 1 | grep -qw {{ .Vars.app_db_name }}"

  - name: create-app-user
    type: exec
    command: "psql -c \"DO \\$\\$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='{{ .Vars.app_db_user }}') THEN CREATE ROLE {{ .Vars.app_db_user }} LOGIN; END IF; END \\$\\$;\""
    user: postgres
    unless: "psql -tAc \"SELECT 1 FROM pg_roles WHERE rolname='{{ .Vars.app_db_user }}'\" | grep -q 1"

  - name: set-app-user-password
    type: exec
    stdin: "secret:vigo/app/db_password"
    command: "psql -c \"ALTER ROLE {{ .Vars.app_db_user }} PASSWORD '$(cat -)';\""
    user: postgres
    watch_secret: ["vigo/app/db_password"]
    when: "changed"

  - name: grant-app-privileges
    type: exec
    command: "psql -c \"GRANT ALL PRIVILEGES ON DATABASE {{ .Vars.app_db_name }} TO {{ .Vars.app_db_user }};\""
    user: postgres
```

## Variables partagées via common.vgo

Les fichiers `common.vgo` au niveau du répertoire définissent des vars partagées pour chaque envoy du sous-arbre. Un `common.vgo` placé à côté des envoys de base de données fournit les valeurs par défaut communes à toute la base de données :

`stacks/db/common.vgo` :

```yaml
vars:
  app_db_name: myapp
  app_db_user: myapp
  pg_allowed_cidr: "10.0.0.0/8"
```

Tout bloc match sous `stacks/db/` hérite de ces vars ; les vars du bloc match les remplacent.

## Définition du rôle

`stacks/roles/database-server.vgo` :

```yaml
name: database-server
configcrates:
  - postgresql
  - app-database
```

## Affectation des nœuds

`stacks/db/envoys.vgo` :

```yaml
envoys:
  - match: "db-prod-*.example.com"
    environment: production
    roles: [database-server]
    vars:
      pg_listen_addresses: "*"

  - match: "db-staging-*.example.com"
    environment: staging
    roles: [database-server]
    vars:
      pg_listen_addresses: "*"
```

Les blocs match restent petits — seules les vars qui ne sont pas spécifiques à l'environnement y figurent.

## Réglage par environnement

`stacks/db/environments.vgo` :

```yaml
env:
  production:
    vars:
      pg_max_connections: "200"
      pg_shared_buffers: 4GB
      pg_effective_cache_size: 12GB
      pg_work_mem: 16MB

  staging:
    vars:
      pg_max_connections: "50"
      pg_shared_buffers: 512MB
      pg_effective_cache_size: 1536MB
      pg_work_mem: 4MB
```

Le réglage spécifique à chaque environnement réside dans un seul fichier, indexé par palier. Ajouter un nouveau palier revient à ajouter un bloc ici — sans toucher aux blocs match.

## Références de secrets

Le préfixe `secret:` dans le paramètre `stdin` de la ressource `set-app-user-password` signifie :

1. Le serveur résout `secret:vigo/app/db_password` via le fournisseur de secrets au moment du check-in
2. La valeur résolue est transmise à l'agent dans le bundle de politique
3. L'agent achemine la valeur vers le stdin de la commande
4. Le mot de passe n'apparaît jamais dans les chaînes de commande, les journaux ou les fichiers de configuration

Pour le backend de secrets `local`, créez le fichier de secret :

```bash
mkdir -p /srv/vigo/secrets/vigo/app
longdrawer -n 'my-secure-password' > /srv/vigo/secrets/vigo/app/db_password
chmod 600 /srv/vigo/secrets/vigo/app/db_password
```

## Récapitulatif des mécanismes utilisés

- `common.vgo` — vars partagées pour chaque envoy d'un sous-arbre (DRY entre les blocs match)
- `vars:` de bloc match — vars qui diffèrent selon le motif match
- `environments.vgo` — vars qui diffèrent selon le palier (`environment:`)
- préfixe `secret:` — identifiants résolus côté serveur au check-in

Voir [Configuration multi-axes](../reference/configcrate-language.md) pour le modèle canonique complet.
---
*Confidentiel -- Alexander4, LLC. Ne pas redistribuer. Voir la [licence](../legal/license.md).*
