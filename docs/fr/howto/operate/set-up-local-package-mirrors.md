<!-- vigo-i18n source_sha:32844da8c4a499a49e0550e40f5b954c678cd1a57a07ef93d53d9c121c261b13 -->

---
title: Mettre en place des miroirs de paquets locaux
---

# Mettre en place des miroirs de paquets locaux

À la fin de cette page, vous aurez un hôte de votre flotte qui réplique une archive de paquets en amont (apt, dnf, apk, zypper, pacman ou bottles Homebrew), le reste de la flotte récupérant les paquets depuis cet hôte par HTTP au lieu de l'internet public.

**Quand l'utiliser :** la flotte récupère sans cesse les mêmes paquets ; la facture de bande passante ou le temps d'amorçage à partir de zéro est réel ; certains Envoys sont sur des liens lents / facturés au volume / pare-feutés et ne peuvent atteindre que des hôtes internes ; vous voulez épingler un instantané connu-bon de l'archive d'une distribution pendant que les mises à jour testées en production se font selon un calendrier.

**Quand l'éviter :** la flotte est assez petite pour que chaque Envoy récupérant depuis l'amont ne pose pas de problème, ou vous disposez déjà d'un proxy de paquets d'entreprise (Artifactory, Pulp, Spacewalk) qui gère ce cas.

## Comment cela s'intègre

Chaque configcrate `<pm>-mirror` est une recette stacks autonome : installer l'outil de miroir amont, écrire sa configuration, l'exécuter via cron, servir l'arborescence résultante par HTTP via nginx. La même forme se répète d'un gestionnaire de paquets à l'autre — seuls l'outillage, l'URL amont et la disposition sur disque changent.

Pas Curator. Pas le Swarm. **Un hôte exécute le miroir amont ; le reste de la flotte pointe son gestionnaire de paquets vers le nginx de cet hôte.** C'est toute l'architecture. La raison d'utiliser un configcrate stacks plutôt que le substrat P2P de Curator ici est que, lorsqu'un hôte possède déjà les octets venus de l'amont, il n'y a aucune raison de les redistribuer aussi en P2P à travers la flotte — l'argument d'économie de bande passante est « récupérer une fois depuis l'internet, servir N fois sur le LAN ».

Associez le configcrate de miroir au [configcrate nginx](../../reference/vigo/resources/file.md) sur le même hôte. Le configcrate de miroir dépose un bloc `server` dans `/etc/nginx/conf.d/` et notifie le service nginx pour rechargement — il n'installe pas nginx lui-même.

## Recettes livrées

Chaque exemple se trouve à `example-configs/stacks/configcrates/system/<pm>-mirror.vgo.example`. Copiez-le dans vos stacks, réglez les vars selon l'amont de votre choix, publiez.

| Configcrate | Famille d'OS | Outillage | Amont | Configuration côté client |
|---|---|---|---|---|
| [`apt-mirror`](../../../example-configs/stacks/configcrates/system/apt-mirror.vgo.example) | Debian / Ubuntu | `apt-mirror` (paquet) + cron + nginx | `archive.ubuntu.com/ubuntu` (ou toute archive Debian) | `deb http://<mirror>/<upstream-host>/<path> <distribution> <components>` dans `/etc/apt/sources.list` |
| [`dnf-mirror`](../../../example-configs/stacks/configcrates/system/dnf-mirror.vgo.example) | RHEL / Fedora / Rocky / AlmaLinux / Amazon Linux | `reposync` (dnf-utils) + `createrepo_c` + cron + nginx | source `dnf` configurée sur l'hôte miroir | `baseurl=http://<mirror>/<repoid>/` dans un fichier `.repo` |
| [`apk-mirror`](../../../example-configs/stacks/configcrates/system/apk-mirror.vgo.example) | Alpine | `rsync` depuis `rsync://rsync.alpinelinux.org/alpine/` + nginx | Alpine officiel | `http://<mirror>/alpine/<version>/main` dans `/etc/apk/repositories` |
| [`zypper-mirror`](../../../example-configs/stacks/configcrates/system/zypper-mirror.vgo.example) | openSUSE / SLES | `dnf reposync` + `createrepo_c` (rpm-md, même forme que dnf) | miroir openSUSE / SLES | `baseurl=http://<mirror>/<repoid>/` dans `/etc/zypp/repos.d/<repo>.repo` |
| [`pacman-mirror`](../../../example-configs/stacks/configcrates/system/pacman-mirror.vgo.example) | Arch Linux | `rsync` depuis un miroir Arch tier-1 + nginx | miroir tier-1 configuré | `Server = http://<mirror>/archlinux/$repo/os/$arch` dans `/etc/pacman.d/mirrorlist` |
| [`brew-bottle-mirror`](../../../example-configs/stacks/configcrates/system/brew-bottle-mirror.vgo.example) | macOS | `proxy_cache` nginx en façade de `ghcr.io/v2/homebrew/core/` | GitHub Container Registry (le magasin de bottles d'Homebrew) | `export HOMEBREW_BOTTLE_DOMAIN=http://<mirror>` sur chaque client |

## Forme différente : `brew-bottle-mirror`

Cinq des six exemples suivent le même schéma — rsync (ou équivalent rsync) depuis l'amont, puis servir l'arborescence de répertoires résultante depuis le disque. `brew-bottle-mirror` est l'exception : les bottles Homebrew vivent sur GitHub Container Registry sous forme de blobs OCI, pas sur une arborescence de miroir rsync-able traditionnelle. La forme pragmatique sur macOS est **un proxy inverse à cache transparent** — nginx avec `proxy_cache_path` en façade de `ghcr.io`. La première récupération va vers GitHub, chaque récupération suivante est servie depuis le disque local. Les clients règlent `HOMEBREW_BOTTLE_DOMAIN` sur l'hôte miroir et `brew install` fait le reste.

Les compromis : pas de hors-ligne dès le premier jour (le premier défaut de cache doit atteindre l'amont), mais pas de rsync préalable non plus (le cache se remplit en réponse à la demande). Pour une flotte typique de portables macOS, c'est la forme opérationnellement correcte.

## Recettes différées — le schéma est documenté, l'exemple n'est pas livré

Ces écosystèmes sont réalisables en principe mais ne sont pas inclus dans le jeu livré actuel. Chaque entrée indique *quel outillage vous utiliseriez* si vous écriviez le configcrate vous-même ; le schéma est identique aux exemples livrés (installer l'outil, cron, nginx).

### Windows

- **Chocolatey** — flux NuGet. Le cas du dépôt interne passe par un serveur NuGet (Chocolatey Server / ProGet / répertoire statique de `.nupkg`). Le drapeau `--internalize` qui permet à `choco download` de réécrire les paquets communautaires pour un service interne est une fonctionnalité Chocolatey Pro. Voie gratuite : servir un répertoire statique de `.nupkg` via IIS ou nginx + `choco push` des paquets que vous avez téléchargés manuellement. Différé conformément à la cadence Linux-first de Vigo — pas de nouveau développement Windows tant qu'un client avec une vraie flotte Windows ne le demande pas.
- **Scoop** — buckets basés sur git. « Mettre en miroir » signifie un miroir git local du dépôt de bucket plus un serveur HTTP devant. Outil à plus petite base d'utilisateurs ; différé pour la même raison.
- **Winget** — API REST + arborescence de manifestes. Le miroir de source signifie héberger votre propre source privée. Réalisable mais plus complexe que les miroirs d'arborescence de fichiers ci-dessus ; différé.

### BSD / illumos

- **FreeBSD `pkg`** — `pkg fetch -d` dans un répertoire + nginx qui le sert ; les clients pointent l'`url` de `pkg` vers le miroir.
- **OpenBSD `pkg_add`** — `rsync` depuis un miroir OpenBSD ; `pkg_add` lit les URL `PKG_PATH`.
- **illumos / Solaris IPS** — `pkgrecv` dans un dépôt local, `pkg.depotd` (ou nginx devant `/var/share/pkg/repositories/`) pour servir. Réalisable ; différé jusqu'à une demande en labo.
- **MacPorts** — rsync depuis MacPorts amont ; moins couramment mis en miroir.

### Registres de langages

Ce sont une catégorie différente — « miroirs d'index de paquets de langage » plutôt que « miroirs de paquets d'OS » — et l'outillage exécute généralement son propre daemon plutôt que de produire une arborescence statique que nginx peut servir. Si vous les voulez, ouvrez un élément séparé :

- **PyPI** → `devpi-server` ou `bandersnatch`
- **npm** → `verdaccio` (registre complet) ou `npm proxy`
- **RubyGems** → `geminabox` ou `rubygems-mirror`
- **Modules Go** → Athens (`GOPROXY`)
- **Cargo** → miroir de registre alternatif personnalisé

### Images de conteneurs

Déjà couvert par le shim Docker Registry v2 de Curator — voir [Mettre en place Curator](../swarm/set-up-curator.md). Ce n'est pas l'affaire d'un configcrate de miroir de paquets.

## Notes opérationnelles

- **Un hôte miroir par famille** est la forme v1. Un seul hôte miroir géré par Vigo peut exécuter plusieurs de ces configcrates à la fois (un par famille) — `apt-mirror` pour les Envoys Ubuntu, `dnf-mirror` pour les Envoys Rocky, `apk-mirror` pour les Envoys Alpine, etc. Ils servent sous des blocs `location` non conflictuels de la même instance nginx et des chemins disque distincts.
- **L'usage disque** est important. Un miroir complet d'Ubuntu main + restricted + universe + multiverse pour une version tourne autour de 250–400 Gio. Prévoyez le disque avant de publier. La directive `clean` d'`apt-mirror` élague les paquets supprimés en amont, mais l'ensemble de travail est l'ensemble de travail.
- **Le calendrier cron** est par défaut nocturne dans chaque exemple (`0 4 * * *`). Sur un lien montant lent, étirez-le à hebdomadaire — la convergence de la flotte n'est pas bloquée par la fraîcheur du miroir, seulement par sa disponibilité.
- **Sources de vérité.** Si le miroir prend du retard (cron en échec, disque plein, amont modifié), les Envoys clients peuvent toujours se mettre à jour — apt/dnf/apk réessaieront depuis les URL amont de la liste de sources. Ajoutez le miroir comme PREMIÈRE source et gardez l'amont en repli. Les exemples mettent cela en place.
- **Convergence de premier lancement.** Le premier rsync/reposync prend des heures et le délai de convergence par ressource de Vigo (300 s par défaut pour les ressources `exec` / `package` / `repository`, selon `agent/src/runner/mod.rs`) tuera une synchro en ligne. Les exemples mettent délibérément en place le cron et laissent la première synchro se faire hors-bande — l'opérateur l'exécute une fois à la main ou attend le premier tic de cron.
- **Rétractation.** Chaque configcrate possède un fichier de rétractation associé à `example-configs/retractions/configcrates/system/<pm>-mirror-retract.vgo.example`. La rétractation supprime l'entrée cron, le site nginx et (le cas échéant) le paquet de l'outil miroir, mais **laisse l'arborescence en miroir sur le disque** — les opérateurs la suppriment manuellement après avoir confirmé que la flotte est revenue à l'amont. La suppression sur disque n'est pas réversible et ne devrait pas être un effet de bord d'un cycle de convergence.

## Quand quelque chose tourne mal

| Symptôme | Cause probable | Où regarder |
|---|---|---|
| Arborescence miroir vide ou périmée | Le premier tic de cron n'a pas encore eu lieu, ou cron a échoué | `journalctl -u cron`, le journal de l'outil miroir sous `/var/log/<pm>-mirror/` |
| Les clients reçoivent des 404 sur les métadonnées de paquets | Bloc `location` nginx erroné, ou disposition de l'archive amont modifiée | `/var/log/nginx/<pm>-mirror.access.log` pour les chemins réellement demandés |
| Disque plein | L'ensemble de travail est plus gros que prévu ; `clean` sur apt-mirror | `df -h /var/spool/<pm>-mirror` ; réduisez la liste des composants si vous n'avez pas besoin de tous |
| Les clients se rabattent sur l'amont à chaque installation | Hôte miroir injoignable, nom de certificat non concordant, ou sources.list pointe d'abord sur l'amont | `apt-cache policy <pkg>` (ou équivalent) pour confirmer quelle source est préférée |
