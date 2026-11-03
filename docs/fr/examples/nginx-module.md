<!-- vigo-i18n source_sha:e5a5286608e874a6f9707c359ea9cf6015a196a42a6c5856ada70f4969034c66 -->

# Configcrate Nginx

Une configcrate nginx complète illustrant le motif paquet-config-service avec des chaînes `notify`.

## Définition de la configcrate

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

## Fonctionnement

1. **nginx-package** installe le paquet nginx. Il s'exécute en premier car le service et la configuration dépendent de la présence du paquet.

2. **nginx-config** écrit le fichier de configuration nginx à l'aide de templates Go. L'attribut `content:` utilise `.Vars.nginx_worker_processes` et d'autres variables qui peuvent être surchargées par Envoy. Le `notify: [nginx-service]` signifie que lorsque le fichier de configuration change, le service nginx est redémarré.

3. **nginx-service** garantit que nginx est en cours d'exécution et activé au démarrage. Il ne redémarre que lorsqu'il est notifié par la ressource du fichier de configuration.

## Affectation de rôle

`stacks/roles/webserver.vgo` :

```yaml
name: webserver
configcrates:
  - nginx
```

## Affectation de nœud

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

## Surcharge des variables

Les variables au niveau du nœud surchargent les valeurs par défaut de la configcrate. Dans cet exemple, la configcrate utilise par défaut `auto` pour les processus de travail et `localhost` comme nom de serveur, mais les serveurs web de production reçoivent `4` travailleurs et le véritable nom de domaine.

## Ajout de SSL

Étendez la configcrate avec un vhost SSL :

```yaml
  - name: nginx-ssl-cert
    type: file
    target_path: /etc/nginx/ssl/server.crt
    owner: root
    group: root
    mode: "0644"
    content: "{{ .Vars.nginx_ssl_cert }}"

  - name: nginx-ssl-key
    type: file
    target_path: /etc/nginx/ssl/server.key
    owner: root
    group: root
    mode: "0600"
    content: "secret:vigo/nginx/ssl_key"
    notify:
      - nginx-service
```

Notez que la clé SSL utilise `secret:` -- le fournisseur de secrets la résout, et le contenu n'est jamais stocké en clair dans les fichiers de configuration.
