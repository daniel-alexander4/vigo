<!-- vigo-i18n source_sha:0425ad11312bfa04c7502f3b7477011c417365514cc6ccdd1b62ff5e2bd68a23 -->

# Écrire votre premier configcrate

À la fin de cette page, vous aurez un configcrate nginx fonctionnel publié sur votre flotte — nginx installé, un fichier de configuration rendu à partir de traits, et le service démarré — appliqué de façon idempotente sur chaque Envoy concerné. La même structure se généralise à tous les configcrates que vous écrirez ensuite : package, file, service, chaîne de dépendances.

**Quand l'utiliser :** tout opérateur qui écrit son premier configcrate Vigo. L'exemple porte sur nginx parce qu'il met en œuvre d'un seul coup les cinq types de ressources les plus courants (`package`, `file`, `service`, `directory`, `exec`) ; remplacez le package et la configuration et vous obtenez un modèle pour postgres, redis, prometheus, n'importe quoi.

**Quand le sauter :** vous avez déjà livré des configcrates pour un autre service ; passez directement à la [référence d'écriture des configcrates](../../reference/configcrate-language.md).

Ce guide construit un configcrate nginx complet avec installation du package, gestion du fichier de configuration et contrôle du service — illustrant les patrons fondamentaux que vous utiliserez dans chaque configcrate Vigo.

## Structure d'un configcrate

Un configcrate est un fichier YAML avec une extension `.vgo` dans `stacks/configcrates/`. Il contient :

```yaml
name: configcrate-name
vars:
  key: default_value
resources:
  - name: resource-name
    type: resource-type
    # type-specific attributes
```

## Étape 1 : Créer le configcrate

Créez `stacks/configcrates/nginx.vgo` :

```yaml
name: nginx

vars:
  nginx_port: 80
  server_name: localhost

resources:
  - name: nginx-package
    type: package
    package: nginx
    state: present

  - name: nginx-config
    type: file
    target_path: /etc/nginx/sites-available/default
    content: |
      server {
          listen {{ .Vars.nginx_port }};
          server_name {{ .Vars.server_name }};
          root /var/www/html;
          index index.html;
      }
    owner: root
    group: root
    mode: "0644"
    depends_on: [nginx-package]
    notify: [restart-nginx]

  - name: nginx-service
    type: service
    service: nginx
    state: running
    enabled: true
    depends_on: [nginx-package]

  - name: restart-nginx
    type: service
    service: nginx
    state: restarted
    when: "changed"
```

## Étape 2 : Comprendre la chaîne de ressources

Ce configcrate illustre trois patrons clés :

**depends_on** — Garantie d'ordonnancement. `nginx-config` ne s'exécutera pas tant que `nginx-package` n'a pas réussi. `nginx-service` attend également `nginx-package`.

**notify + when: "changed"** — Déclenchement sur changement. Lorsque `nginx-config` modifie le fichier, il notifie `restart-nginx`. La ressource `restart-nginx` a `when: "changed"`, donc elle ne se déclenche que lorsqu'elle est notifiée — elle ne redémarre pas à chaque cycle de convergence. C'est le patron standard : `notify` sur la source, `when: "changed"` sur la cible.

**Deux ressources service** — `nginx-service` garantit que nginx tourne et est activé (idempotent — ne démarre que s'il est arrêté). `restart-nginx` gère les redémarrages lors d'un changement de configuration (non idempotent — encadré par `when: "changed"`). Les séparer est le patron recommandé.

**Templates** — L'attribut `content:` utilise la syntaxe de templates Go. `{{ .Vars.nginx_port }}` est remplacé par la valeur de la variable `nginx_port` au moment du check-in.

![Graphe de dépendances des ressources](../../img/resource-dag.svg)

## Étape 3 : Affecter à un Envoy

Créez un répertoire pour vos serveurs web et ajoutez une entrée d'Envoy :

```bash
mkdir -p stacks/web
```

```yaml
# stacks/web/web.vgo
envoys:
  - match: "web*.example.com"
    configcrates: [nginx]
    vars:
      nginx_port: 8080
      server_name: "web.example.com"
```

Les `vars` au niveau du nœud surchargent les `vars` par défaut du configcrate. Ici, `nginx_port` devient `8080` au lieu de la valeur par défaut `80`.

**Astuce :** Si vous avez des configcrates qui devraient s'appliquer à *tous* vos Envoys (comme SSH et la supervision), placez-les dans `stacks/common.vgo` — tout ce qui se trouve dans les sous-répertoires en hérite automatiquement. Voir [Héritage de répertoire](../../reference/configcrate-language.md#directory-inheritance).

## Étape 4 : Utiliser les rôles pour la réutilisation

Pour plusieurs configcrates, regroupez-les dans un rôle. Créez `stacks/roles.vgo` :

```yaml
roles:
  - name: webserver
    configcrates:
      - nginx
      - logrotate-nginx
      - monitoring-node-exporter
```

Puis affectez le rôle :

```yaml
envoys:
  - match: "web*.example.com"
    roles: [webserver]
    vars:
      nginx_port: 8080
```

## Étape 5 : Configuration spécifique à l'environnement

Déclarez `environment:` sur chaque bloc match, puis placez les vars spécifiques à l'environnement dans `environments.vgo` au même niveau de répertoire :

```yaml
# envoys.vgo
envoys:
  - match: "prod-*.web.example.com"
    environment: production
    roles: [webserver]
  - match: "stg-*.web.example.com"
    environment: staging
    roles: [webserver]
```

```yaml
# environments.vgo
env:
  production:
    vars:
      nginx_port: 443
  staging:
    vars:
      nginx_port: 8080
```

Lorsque l'environnement de l'Envoy est `production`, `nginx_port` se résout en `443`. Les surcharges d'environnement sont centralisées dans un seul fichier — chaque palier contribue un bloc, les blocs match restent exempts de bruit par environnement.

## Étape 6 : Ressources conditionnelles

Utilisez `when:` pour appliquer des ressources conditionnellement :

```yaml
resources:
  - name: nginx-package-debian
    type: package
    package: nginx
    when: "os_family('debian')"

  - name: nginx-package-redhat
    type: package
    package: nginx
    when: "os_family('redhat')"
```

L'agent évalue les expressions `when:` à partir des traits de l'Envoy. Seule la ressource correspondante s'exécute.

## Étape 7 : Publier et converger

Publiez la configuration pour l'activer :

```bash
vigocli config publish
```

La commande publish fait passer votre configcrate par le **linter de configcrates**, qui :
- Corrige les problèmes YAML courants (tabulations, espaces en fin de ligne, booléens non entre guillemets)
- Normalise l'ordre des clés et l'indentation
- Valide les champs obligatoires, les références croisées et l'idempotence
- Ajoute des auto-commentaires `#~` décrivant chaque ressource

Après publication, votre fichier de configcrate aura des commentaires `#~` comme ceci :


```yaml
#~ Manages package, file, service
name: nginx
...
resources:
  #~ Install nginx package
  - name: nginx-package
    ...
```

Ces commentaires `#~` sont régénérés à chaque publication — ne les modifiez pas. Utilisez de simples commentaires `#` pour vos propres notes.

L'Envoy prend la modification en compte à son prochain check-in, dans l'intervalle `checkin.interval` défini dans `server.yaml` (cinq minutes dans la configuration initiale).

Vérifiez le résultat de l'exécution :

```bash
vigocli runs list --limit 1
```

Affichez la configuration appliquée de l'Envoy :

```bash
vigocli envoys show web1.example.com
```

## Et ensuite

- [Format de configuration](../../reference/configcrate-language.md) — Référence complète des configcrates, rôles, vars, environments.vgo
- [DAG des ressources](../../reference/configcrate-language.md) — depends_on, notify, subscribes en détail
- [Expressions When](../../reference/configcrate-language.md) — Référence de la logique conditionnelle
- [Templates](../../reference/configcrate-language.md) — Syntaxe de templates Go pour les attributs content:
- [Exécuteur File : stream_edit](../../reference/vigo/resources/file.md#stream-edit) — Faire passer le contenu d'un fichier par des scripts de transformation avant écriture

## À quoi ressemble le succès

Après `vigocli config publish` et un cycle de check-in, le configcrate s'applique proprement sur toute la flotte :

```text
$ docker exec vigo /usr/local/bin/vigocli runs list --limit 5
ID                                    ENVOY                                 STARTED           STATUS   CHANGED  FAILED
b791b14c-d9c9-4699-979f-0be8e232f930  d1f86266-139f-419a-b760-902c0616f586  2026-05-14 00:21  success  0        0
be3f86c5-c809-4989-a014-20a9d9647b2b  cb4acb1f-f995-4026-a01e-f5390283ca6c  2026-05-14 00:21  success  0        0
4f732d0f-c442-4c0c-81fc-b3352514de7c  d1f86266-139f-419a-b760-902c0616f586  2026-05-14 00:20  success  0        0
```

`CHANGED=0` après le premier cycle signifie que le configcrate a convergé et que le cycle suivant est un no-op — c'est l'idempotence à l'œuvre. Un compte `FAILED` non nul signifie que l'exécution d'un Envoy a vu au moins une ressource refuser de converger ; explorez via la page de détail de l'Envoy ou `vigocli runs show <run_id>`.

![Page de détail par Envoy — configcrates, dernière exécution, indicateur de dérive](../../img/screenshots/vigo.envoy-detail.png)

## Et ensuite

- **Ajouter d'autres configcrates** → affectez par patron de nom d'hôte ou par rôle, voir [Publier votre configuration](publish-config.md).
- **Faire varier les configcrates par hôte / OS / environnement** → [la référence d'écriture des configcrates](../../reference/configcrate-language.md) couvre les conditionnels, les lookups, foreach et les surcharges par environnement.
- **Une ressource ne s'est pas appliquée** → [Dépanner les problèmes courants](../operate/troubleshoot.md#configcrate-convergence-loop--same-resource-changes-every-cycle).

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*
