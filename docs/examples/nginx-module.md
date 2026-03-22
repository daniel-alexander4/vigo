
# Nginx Module

A complete nginx module demonstrating the package-config-service pattern with `notify` chains.

## Module Definition

`stack/stage/modules/nginx.vgo`:

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

## How It Works

1. **nginx-package** installs the nginx package. This runs first because the service and config depend on the package being present.

2. **nginx-config** writes the nginx configuration file using Go templates. The `content:` attribute uses `.Vars.nginx_worker_processes` and other variables that can be overridden per-envoy. The `notify: [nginx-service]` means that when the config file changes, the nginx service is restarted.

3. **nginx-service** ensures nginx is running and enabled at boot. It only restarts when notified by the config file resource.

## Role Assignment

`stack/stage/roles/webserver.vgo`:

```yaml
name: webserver
modules:
  - nginx
```

## Node Assignment

`stack/stage/envoys/nodes.vgo`:

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

## Overriding Variables

Node-level vars override module defaults. In this example, the module defaults to `auto` worker processes and `localhost` server name, but the production web servers get `4` workers and the real domain name.

## Adding SSL

Extend the module with an SSL vhost:

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

Note that the SSL key uses `secret:` -- the secrets provider resolves it, and the content is never stored in plaintext in the config files.
