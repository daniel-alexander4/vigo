<!-- vigo-i18n source_sha:9ea42acf9f77a8726bd4830e5e0571d720073b64c44715d63f8dcb4b5e11b085 -->

# Configuration multi-environnement

Cet exemple montre comment maintenir des valeurs de variables différentes pour la production et la préproduction tout en partageant les mêmes configcrates. Le mécanisme canonique de Vigo pour les valeurs spécifiques à un environnement est `environments.vgo` au niveau de répertoire approprié — l'environnement est déclaré sur chaque bloc match, et les variables spécifiques à un environnement sont centralisées dans un fichier voisin.

## Définition de la configcrate

`stacks/configcrates/webapp.vgo` :

```yaml
name: webapp
vars:
  app_port: "8080"
  app_workers: "2"
  app_log_level: info
  app_domain: localhost
  app_db_host: localhost
resources:
  - name: webapp-config
    type: file
    target_path: /etc/webapp/config.yaml
    owner: webapp
    group: webapp
    mode: "0640"
    content: |
      port: {{ .Vars.app_port }}
      workers: {{ .Vars.app_workers }}
      log_level: {{ .Vars.app_log_level }}
      domain: {{ .Vars.app_domain }}
      database:
        host: {{ .Vars.app_db_host }}
        password: {{ .Vars.app_db_password }}
    notify:
      - webapp-service

  - name: webapp-service
    type: service
    service: webapp
    state: running
    enabled: true
```

## Les blocs match déclarent l'environnement

`stacks/envoys.vgo` :

```yaml
envoys:
  - match: "prod-*.web.example.com"
    environment: production
    roles: [webserver]

  - match: "stg-*.web.example.com"
    environment: staging
    roles: [webserver]
```

Chaque bloc match déclare le palier via `environment:`. Aucune variable spécifique à un environnement n'est définie en ligne sur le bloc match lui-même.

## Surcharges par environnement

`stacks/environments.vgo` (voisin de `envoys.vgo`, ou dans n'importe quel répertoire ancêtre) :

```yaml
env:
  production:
    vars:
      app_port: "8080"
      app_workers: "8"
      app_log_level: warn
      app_domain: example.com
      app_db_host: db-prod.internal
      app_db_password: "secret:vigo/webapp/prod/db_password"

  staging:
    vars:
      app_port: "8080"
      app_workers: "2"
      app_log_level: debug
      app_domain: staging.example.com
      app_db_host: db-staging.internal
      app_db_password: "secret:vigo/webapp/staging/db_password"
```

## Comment la résolution s'opère

Au moment du check-in, le serveur :

1. Compare le nom d'hôte de l'envoy à `envoys.vgo` (le premier match l'emporte) pour déterminer le rôle et `environment:`.
2. Étend les rôles en une liste de configcrates et charge les valeurs par défaut des configcrates.
3. Applique les variables héritées de `common.vgo` (parent → enfant) par-dessus les valeurs par défaut des configcrates.
4. Applique les variables du bloc match (s'il y en a — dans cet exemple il n'y en a aucune).
5. Applique par-dessus les variables de `environments.vgo` correspondant à `environment:` du bloc.

`environments.vgo` est limité par son emplacement dans le système de fichiers : un fichier situé à `stacks/customerA/environments.vgo` n'affecte que les envoys dont le hostcrate réside à `stacks/customerA/` ou plus profond.

## Des secrets distincts par environnement

Comme le mot de passe de la base de données réside lui aussi dans `environments.vgo`, chaque environnement obtient naturellement son propre chemin de secret — aucune duplication par bloc match n'est nécessaire. Le fournisseur de secrets résout le préfixe `secret:` au moment du check-in.
