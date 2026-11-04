<!-- vigo-i18n source_sha:e081d86327e66a2dc7f48c0d237fb2f3bee2893388f4233628565e27afcf2492 -->

# Migrer depuis une version sous licence de Vigo

Vigo 0.76.95 et les versions suivantes sont open source sous Apache-2.0, sans limite de nœuds. Les versions antérieures exigeaient un fichier `.lic` signé et imposaient un nombre maximal de nœuds. Cette liste de contrôle fait passer un serveur d'une version sous licence à la version open source actuelle. Votre base de données, vos secrets, votre matériel TLS, vos envoys enrôlés et vos stacks sont conservés.

Le serveur actuel refuse de démarrer si `server.yaml` contient encore une clé qu'il ne connaît pas, y compris l'ancien bloc `license:`. Traitez l'étape server.yaml avant de redémarrer.

## Avant de commencer

- [ ] Faites une sauvegarde : `sudo vigocli backup create /root/vigo-pre-migration.tar.gz --encrypt` (sans `--encrypt`, l'archive n'inclut pas la clé maîtresse des secrets), ou copiez `/srv/vigo/` serveur arrêté. Voir [Sauvegarde et restauration](../operate/backup-and-recovery.md).
- [ ] Gardez une copie de votre fichier `.lic` hors de `/srv/vigo/` si vous voulez en conserver une trace. Vigo ne le lit plus.
- [ ] Installez Docker, Go 1.25 et une chaîne d'outils Rust sur l'hôte du serveur. Aucune image précompilée n'est encore publiée : vous compilez l'image depuis les sources.

## Compiler la nouvelle version

- [ ] Clonez les sources : `git clone https://github.com/daniel-alexander4/vigo.git && cd vigo`
- [ ] Compilez sans déployer : `sudo scripts/upgrade.sh full --norun`. Ajoutez `arm64` (`sudo scripts/upgrade.sh full arm64 --norun`) si vous avez des envoys arm64. Cette étape installe aussi le nouveau `vigocli` sur l'hôte.

## Nettoyer server.yaml

- [ ] Supprimez le bloc `license:` (il contenait `mode: local` ou `mode: aggregate`).
- [ ] Supprimez `license_dir:` de la section `paths:`.
- [ ] Supprimez un éventuel bloc `webhooks:` de premier niveau. Les notifications sortantes se configurent sous `integrations:` ; voir [Configuration du serveur](../../reference/server-yaml.md).
- [ ] Vérifiez le fichier avec le nouveau `vigocli`, lancé depuis `/srv/vigo` : `sudo vigocli doctor --pre-start --config /srv/vigo/server.yaml`. Une ligne `field <nom> not found` désigne une clé qu'il reste à supprimer.

## Déployer

- [ ] Déployez l'image compilée : `sudo scripts/upgrade.sh deploy`. Préférez cette commande à un simple `docker compose up -d` : l'ancien conteneur réécrit `docker-compose.yml` à partir de sa propre image lorsqu'il redémarre.
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
