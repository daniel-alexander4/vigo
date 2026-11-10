<!-- vigo-i18n source_sha:9f0940fbecd4ab918a6ed3becc5bd4dda8a891907497e5d51805751c77a3de02 -->

# Migrer depuis une version sous licence de Vigo

Vigo 0.76.95 et les versions suivantes sont open source, sans limite de nœuds. Les versions antérieures exigeaient un fichier `.lic` signé et imposaient un nombre maximal de nœuds. Cette liste de contrôle fait passer un serveur d'une version sous licence à la version open source actuelle. Votre base de données, vos secrets, votre matériel TLS, vos envoys enrôlés et vos stacks sont conservés.

Le serveur actuel refuse de démarrer si `server.yaml` contient encore une clé qu'il ne connaît pas, y compris l'ancien bloc `license:`. Traitez l'étape server.yaml avant de redémarrer.

## Avant de commencer

- [ ] Faites une sauvegarde : `sudo vigocli backup create /root/vigo-pre-migration.tar.gz --encrypt` (sans `--encrypt`, l'archive n'inclut pas la clé maîtresse des secrets), ou copiez `/srv/vigo/` serveur arrêté. Voir [Sauvegarde et restauration](../operate/backup-and-recovery.md).
- [ ] Gardez une copie de votre fichier `.lic` hors de `/srv/vigo/` si vous voulez en conserver une trace. Vigo ne le lit plus.
- [ ] Vérifiez que l'hôte du serveur dispose de Docker avec le plugin Compose. Compiler depuis les sources plutôt que de récupérer l'image publiée demande en plus le plugin Buildx, `git` et `rsync`.

## Obtenir la nouvelle version

Récupérez l'image publiée ou compilez-la depuis les sources, l'un ou l'autre.

- [ ] Pour la récupérer : `sudo docker pull ghcr.io/daniel-alexander4/vigo:latest`, puis `sudo docker tag ghcr.io/daniel-alexander4/vigo:latest vigo:latest`. Elle contient l'agent Linux pour amd64 et arm64. Installez son `vigocli` sur l'hôte : `id=$(sudo docker create vigo:latest) && sudo docker cp "$id":/usr/local/bin/vigocli /usr/local/bin/vigocli && sudo docker rm "$id"`.
- [ ] Pour la compiler : clonez les sources (`git clone https://github.com/daniel-alexander4/vigo.git && cd vigo`) et compilez sans déployer : `sudo scripts/upgrade.sh full --norun`. Ajoutez `arm64` (`sudo scripts/upgrade.sh full arm64 --norun`) si vous avez des envoys arm64. Cette étape installe aussi le nouveau `vigocli` sur l'hôte.

## Nettoyer server.yaml

- [ ] Supprimez le bloc `license:` (il contenait `mode: local` ou `mode: aggregate`).
- [ ] Supprimez `license_dir:` de la section `paths:`.
- [ ] Supprimez un éventuel bloc `webhooks:` de premier niveau. Les notifications sortantes se configurent sous `integrations:` ; voir [Configuration du serveur](../../reference/server-yaml.md).
- [ ] Vérifiez le fichier avec le nouveau `vigocli`, lancé depuis `/srv/vigo` : `sudo vigocli doctor --pre-start --config /srv/vigo/server.yaml`. Une ligne `field <nom> not found` désigne une clé qu'il reste à supprimer.

## Déployer

- [ ] Déployez la nouvelle image. Avec une copie des sources, lancez `sudo scripts/upgrade.sh deploy`. Avec l'image récupérée, lancez `cd /srv/vigo && sudo docker compose down`, puis `sudo docker run --rm -v /srv/vigo:/srv/vigo vigo:latest --seed-only`, puis `sudo docker compose up -d`. N'utilisez pas un simple `docker compose up -d` seul : l'ancien conteneur réécrit `docker-compose.yml` à partir de sa propre image lorsqu'il redémarre.
- [ ] Suivez le premier démarrage : `docker logs -f vigo`. Les migrations de la base s'exécutent avant que le serveur affiche `server ready` et peuvent prendre plusieurs minutes sur une grosse base. Le conteneur apparaît « unhealthy » jusqu'à la fin ; laissez-le tourner.
- [ ] Vérifiez que le serveur répond : `curl -sk https://127.0.0.1:8443/healthz` renvoie `200`.

## Supprimer ce qu'utilisait la licence

- [ ] Supprimez le répertoire de licence : `sudo rm -r /srv/vigo/license`.
- [ ] Retirez `vigocli license ...` de vos scripts et procédures. La commande n'existe plus.
- [ ] Dans Prometheus, supprimez le groupe d'alertes `vigo-license` et toute règle ou tout panneau Grafana qui utilise une métrique `vigo_license_*`. Pour la taille du parc, utilisez `vigo_nodes_total` et `vigo_nodes_active_total`.
- [ ] Si vous utilisez Connwaer, retirez `license_dir` de sa configuration. Connwaer 0.1.5 et suivants l'ignorent.

## Réenrôler les envoys concernés

- [ ] Lancez `sudo vigocli doctor`. Il liste les envoys sans empreinte de certificat client enregistrée. Le serveur actuel les refuse au check-in.
- [ ] Sur chaque envoy listé, relancez le bootstrap : `curl -sSfk https://<serveur>:8443/bootstrap | sudo sh`. Le réenrôlement enregistre l'empreinte.
- [ ] Les envoys enrôlés sous une version sous licence conservent aussi un `COMMERCIAL-TERMS.md` et un `LICENSE` obsolète dans leur répertoire de données (`/etc/vigo-envoy/` sous Linux, `C:\ProgramData\Vigo\` sous Windows). Un nouveau bootstrap les remplace par les fichiers `LICENSE` et `NOTICE` d'Apache-2.0. Sur les envoys que vous ne réenrôlez pas, vous pouvez supprimer `COMMERCIAL-TERMS.md` à la main.

## Vérifier le résultat

- [ ] `sudo vigocli doctor` ne signale plus aucun envoy sans empreinte.
- [ ] `sudo vigocli envoys list` montre que chaque envoy refait ses check-ins.
- [ ] Les envoys mettent d'eux-mêmes leur agent à la version du serveur au fil des check-ins suivants.

La fédération Spanner n'a plus besoin de licence. Si vous utilisiez `license.mode: aggregate` entre bolts, rien ne le remplace : chaque bolt enrôle sans plafond.
