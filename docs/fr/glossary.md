<!-- vigo-i18n source_sha:38a3e7cb3437e5779d5426ddb35c413ec94857c1fc7e2637cea28c9d5651ddf9 -->

# Glossaire

Définitions canoniques des termes propres à Vigo. Par ordre alphabétique. Chaque entrée est un paragraphe avec des liens croisés vers la page qui couvre le concept en profondeur.

Si vous rencontrez un terme dans la documentation ou la CLI qui ne figure pas ici, c'est un bug de documentation — merci de le signaler.

## blob

Une séquence d'octets opaque distribuée par le sous-système [swarm](howto/swarm/set-up-swarm.md), adressée par son hachage SHA-256. Un blob est immuable : redistribuer du contenu mis à jour signifie semer un nouveau blob et révoquer l'ancien. Les agents stockent les blobs dans `/var/lib/vigo/swarm/.blobs/` et matérialisent des liens symboliques vers eux sous `/var/lib/vigo/swarm/files/` selon les chemins cibles du manifeste.

## bundle

Une archive de dépôt git autonome produite par `git bundle create`, utilisée comme unité de distribution de [gitback](howto/swarm/set-up-gitback.md). Les bundles peuvent être complets (historique complet du dépôt) ou incrémentaux (deltas depuis un bundle précédent). Les destinataires matérialisent les bundles en dépôts nus à `/var/lib/vigo/swarm/gitback/mirrors/<seeder>/<user>/<project>.git`.

## compliance

Couverture des cadres normatifs. Se divise en deux surfaces, gardées distinctes depuis la version 0.32.0 :

- **Conformité de l'Envoy** — déterminer si les configcrates d'un nœud géré satisfont les contrôles de cadres réglementaires (HIPAA, SOC 2, PCI DSS, NIST 800-53, ISO 27001, etc.). Mesurée en pourcentage de couverture par cadre. Un contrôle est considéré comme couvert si et seulement si (a) l'Envoy possède un configcrate étiqueté avec ce contrôle via `compliance:`, (b) une vérification de trait [Connwaer](#connwaer) sur l'Envoy l'escalade, ou (c) un [waiver](#waiver) s'applique. C'est la surface que mesurent les rapports de cadre sous `/api/v1/report/<slug>`.
- **Conformité produit de Vigo** — ce que le serveur et l'agent Vigo satisfont par construction (mTLS, RBAC, chaîne de hachage d'audit, chiffrement des secrets, etc.). Se trouve dans `server/compliance/AppControls` et est exposée séparément à `/platform/compliance` (Platform → Data). Utile pour les questionnaires fournisseurs ; jamais intégrée au scoring des cadres.

Distincte de la [convergence](#convergence) : la convergence répond à « mes configcrates se sont-ils appliqués avec succès », la conformité à « mes configcrates satisfont-ils le cadre X ». Voir [scope](#scope) pour la classification de chaque contrôle de cadre, et [Compliance](reference/compliance-matrix.md) pour la vue d'ensemble complète.

## connwaer

Un agent Rust autonome pour la vérification active de conformité — contrôler des éléments que Vigo ne peut pas prouver par la seule application (stockage WORM, validation RNG, chiffrement au repos, segmentation réseau). S'exécute aux côtés de l'agent vigo sur les Envoys enrôlés. Produit distinct avec son propre fichier de licence et un comptage de nœuds indépendant. Voir la [documentation Connwaer](reference/compliance-matrix.md).

## convergence

État de convergence des configcrates pour un Envoy, évalué sur deux axes orthogonaux :

- **Axe d'échec** — `converged`, `degraded`, `failed`, `no_data`. Mutuellement exclusifs ; leur somme égale le total de la flotte. Tout échec non nul franchit le seuil ; zéro échec donne `converged`, quel que soit le nombre de configcrates ayant effectué des changements.
- **Axe de dérive** — `none`, `changed`, `diverged`. `changed` se déclenche lorsque la dernière exécution a `ChangedCount > 0` ; `diverged` se déclenche après `DivergedThreshold` exécutions consécutives avec changements (5 par défaut). Diverged est un sous-ensemble strict de changed. Indépendant de l'axe d'échec — un Envoy peut être `converged` ET `diverged` simultanément (zéro échec, mais churn persistant des configcrates).

L'accessibilité (en ligne / hors ligne) est un troisième axe orthogonal suivi par `server/freshness`. Voir le [parcours de la convergence](reference/configcrate-language.md).

## drift

L'axe de dérive de la convergence (ci-dessus). Calculé par `convergence.ComputeDrift` sur l'historique d'exécutions récent d'un Envoy, exprimé sous forme de `DriftLevel` (`none` / `changed` / `diverged`). La dérive concerne uniquement le fait *que les configcrates continuent de muter* — elle ne dit rien sur la réussite des mutations. Un configcrate non idempotent qui recrée le même fichier à chaque exécution produit des résultats `Created` persistants et place l'Envoy en `diverged` même si chaque exécution réussit techniquement.

## envoy

Un nœud géré exécutant l'agent Rust `vigo`. Chaque Envoy s'enrôle une fois, reçoit un UUID client, et fait un check-in périodique pour récupérer l'état désiré et rapporter les résultats. Le serveur central adresse les Envoys individuels par UUID ; les opérateurs les référencent par nom d'hôte. Le parcours retirer/remplacer/éliminer utilise trois verbes différents (revoke, puddle evict, delete) avec des portées délibérément différentes — voir la référence [cycle de vie de l'Envoy](howto/operate/troubleshoot.md).

## executor

L'implémentation Rust d'un type de ressource dans l'agent (`file`, `package`, `service`, `user`, `cron`, etc.). Les executors vérifient l'état actuel, n'agissent que sur la dérive (idempotents), et renvoient l'un de `None`/`Created`/`Updated`/`Deleted`/`Skipped`/`Blocked`. Voir la [référence des executors](reference/vigo/resources/).

## fleet

Tous les [Envoys](#envoy) enrôlés auprès d'un serveur donné. « À l'échelle de la flotte » signifie *sur l'ensemble de ces Envoys* — c'est la portée la plus large dans Vigo. À distinguer de deux portées plus étroites que l'UI ne doit pas confondre avec elle :

- Le **[swarm](#swarm)** est le substrat P2P reliant les Envoys entre eux ; en pratique le swarm couvre les mêmes Envoys que la flotte, mais c'est la *couche de transport*, pas l'ensemble des machines. Il y a exactement un seul swarm — il n'existe pas de « swarm d'un utilisateur ».
- Un **[puddle](#puddle)** est une unité de confiance *par utilisateur* : la collection d'Envoys d'un seul utilisateur, traitée comme une identité Ed25519 unique. « Le puddle d'un utilisateur » a du sens ; « le puddle de la flotte » n'en a pas.

Mots de portée canoniques, du plus étroit au plus large : **puddle** ⊆ Envoys-exécutant-un-utilisateur-donné ⊆ **fleet** ; le **swarm** est le substrat au-dessus de la flotte. (« Mesh » n'est *pas* un terme Vigo canonique — là où une prose plus ancienne disait « mesh » elle désignait soit le puddle (pour [lockbox](#lockbox)) soit un ensemble de synchronisation par utilisateur ad hoc (pour [longdrawer](#longdrawer)) ; utilisez le terme précis.)

Voir le [diagramme de hiérarchie des portées](howto/swarm/set-up-swarm.md#scope-fleet-vs-swarm-vs-puddle) sur la page Swarm pour la relation entre ces couches et les quatre sous-systèmes de contenu.

## filecast

Distribution de fichiers poussée par l'administrateur au-dessus du substrat [swarm](#swarm). Un opérateur exécute `vigocli swarm filecast distribute <file>` (ou dépose un fichier + un sidecar `.swarm` sur un seeder) et le blob se réplique à travers la flotte via le canal pair du swarm. Filecast est l'un des quatre sous-systèmes de contenu qui s'appuient sur le swarm ; les trois autres ([longdrawer](#longdrawer), [gitback](#gitback), [lockbox](#lockbox)) sont à portée utilisateur, tandis que filecast est à portée opérateur et à l'échelle de la flotte. Exposé dans l'interface web à [`/swarm/filecast`](howto/).

## manifest

Le magasin de métadonnées LWW-CRDT des blobs du swarm, propagé entre les Envoys. Suit quels blobs existent, leurs cibles, les politiques de rétention et les pierres tombales de révocation. Chaque Envoy détient un manifeste local ; les divergences de version pendant le multicast déclenchent une synchronisation du manifeste sur le canal HTTPS pair. Voir [Manifeste du Swarm](howto/swarm/set-up-swarm.md#manifest).

## hostcrate

Tout fichier `.vgo` contenant un bloc `envoys:` avec des entrées `- match:`. Mappe des motifs/globs de nom d'hôte vers des rôles, des configcrates et des vars. Premier match gagnant. Nommé conventionnellement `envoys.vgo` mais le chargeur parcourt tous les fichiers `.vgo`, donc des noms de fichiers par hôte (`plex.vgo`, `web01.vgo`) fonctionnent aussi. Voir [Configuration multi-axes](reference/configcrate-language.md).

## configcrate

Un ensemble de ressources réutilisable dans `stacks/configcrates/*.vgo`. Déclare des ressources, des vars par défaut et des revendications de conformité optionnelles. Composé en [rôles](#role), appliqué aux [Envoys](#envoy) via des [hostcrates](#hostcrate) ou l'héritage `common.vgo` au niveau du répertoire. Les configcrates sont l'unité de composition principale pour la configuration à l'échelle de l'Envoy. Voir [Format de configuration](reference/configcrate-language.md) et [Langage des ressources](reference/configcrate-language.md).

## resource

Une déclaration d'état idempotente unique à l'intérieur d'un configcrate — un fichier, un paquet, un service, un compte utilisateur, etc. Les ressources déclarent un `type:` (mappé à un [executor](#executor)), des paramètres, et des attributs optionnels `depends_on` / `notify` / `when:`. Voir [Langage des ressources](reference/configcrate-language.md).

## retract

Un inverse auto-généré d'un configcrate — les ressources fichier basculées en `state: absent`, les services en `stopped`+`enabled: false`, les paquets en `absent`, etc. Utilisé pour une suppression propre de configcrate. Généré par `vigocli config publish` lorsque la rétraction est activée ; le fichier `.retract` résultant est inerte jusqu'à ce qu'il soit renommé et appliqué. Voir [Rétraction de configcrate](reference/configcrate-language.md).

## role

Un regroupement nommé de configcrates et de vars défini dans `roles.vgo` (ou le répertoire hérité `roles/`). Prend en charge `when:` (conditionnel à l'échelle du rôle), `case:` (groupes de configcrates par plateforme), et `includes:` (composition de rôles à un seul niveau). Assigné aux Envoys via les [hostcrates](#hostcrate). Voir [Format de configuration](reference/configcrate-language.md).

## sandgorgon

Le sous-système de cycle de vie bare metal de Vigo : provisionnement PXE, intégration BMC (Redfish), workflow de mise hors service avec génération de certificat NIST 800-88, import d'inventaire CSV/NetBox. Exposé à `/sandgorgon/` dans l'interface web et via `vigocli sandgorgon`. **Reporté après la v1** -- le code est présent mais le cycle de vie bare metal n'est pas une fonctionnalité prise en charge en v1. Voir [Sandgorgon](overview.md).

## scrier

Le sous-système d'accès distant administrateur de Vigo : sessions interactives SSH, RDP et VNC vers n'importe quel Envoy enrôlé, routées à travers l'`AgentStream` sortant de l'Envoy + un `TunnelStream` dédié. Aucun port entrant côté Envoy — la proposition de valeur centrale pour les MSP/pare-feux clients. Accès navigateur à `/scrier/{envoy_id}` ; accès CLI via `vigocli scrier ssh`. Voir [Scrier](howto/operate/set-up-scrier.md).

## seed

L'instance d'origine d'un blob swarm sur un Envoy — la source de vérité pour ce blob. Aujourd'hui les seeds sont créés exclusivement par `vigocli swarm filecast distribute`, qui écrit dans le répertoire de seed monté en bind du serveur. Le verbe de seed d'origine Envoy (`vigo swarm seed add`) est reporté — voir `pending.md` pour le statut. Les pairs tirent des chunks des seeds jusqu'à avoir des copies locales complètes.

## spanner

L'ensemble du plan de contrôle fédéré — une constellation de serveurs [bolt](#bolt) coopérant pour opérer une seule flotte. La CLI (`vigocli spanner`), la route UI (`/spanner`), l'espace de noms REST (`/api/v1/spanner/*`), la clé YAML (`spanner:`) et le préfixe de métrique (`vigo_spanner_*`) parlent tous le vocabulaire spanner. L'implémentation filaire actuelle est hub + bolts ; la phase 1+ la remplacera par du gossip pair-à-pair égalitaire. Le chargeur rejette la clé YAML retirée `tungol:` au démarrage. Voir [Configurer Spanner](howto/operate/set-up-spanner.md).

## bolt

Un `vigosrv` participant à une fédération [spanner](#spanner). Possède une partition de la flotte par motif de nom d'hôte ; gère les check-ins d'agents pour ses Envoys ; participe comme pair dans le CRDT d'admissions propagé par gossip (ADR-026). La configuration YAML utilise `spanner.mode: founder` (le rendez-vous de découverte d'amorçage en phase 2 — la phase 3 retire entièrement la distinction founder/joiner) ou `spanner.mode: joiner` (un bolt qui a rejoint un spanner existant). L'identité par bolt est la clé publique Ed25519 de `server/bolt/`, pas un ID configuré en YAML.

## stacks

L'arborescence unique éditée par l'opérateur sous [stacks](#stacks), à `/srv/vigo/stacks/`. Contient toutes les primitives de configuration : [configcrates](#stone) (définitions de configcrate), [usercrates](#usercrate), `templates/` (fichiers de templates Go référencés par `source:`), `tasks/`, `workflows/`, ainsi que les fichiers d'assignation `envoys.vgo` hostcrates, `roles.vgo`, `common.vgo`, `waivers.vgo`, `environments.vgo`, et les fichiers d'héritage de revendications `compliance.vgo`. La fusion 0.33.1 a intégré la sous-arborescence `stacks/` précédente dans stacks — il n'y a plus qu'une seule arborescence désormais, et `vigocli config publish` ne synchronise que celle-ci vers [.live](#live).

## stone

Un fichier de définition de configcrate dans `stacks/configcrates/**/*.vgo`. Le nom de répertoire `configcrates/` est la métaphore de construction au niveau du système de fichiers pour ce que le YAML appelle un [configcrate](#configcrate) — la clé YAML est `configcrates:`, et l'identifiant Go/Rust est `ConfigcrateDefinition`. Les opérateurs écrivant `configcrates: [nginx]` dans un rôle référencent le stone à `stacks/configcrates/nginx.vgo`.

## swarm

Le substrat de réseau P2P réservé aux Envoys. Le swarm est la couche de transport sur laquelle s'appuient tous les sous-systèmes de contenu P2P : découverte de pairs (multicast sur `224.0.0.42:1531` + indices adossés au registre), HTTPS mTLS entre Envoys sur le port 1531, gossip CRDT du manifeste, et chunking adressé par contenu. Le swarm ne définit pas lui-même *ce qui* est distribué — ce sont les quatre sous-systèmes de contenu qui le font : [filecast](#filecast) (blobs poussés par l'admin, à l'échelle de la flotte), [longdrawer](#longdrawer) (synchronisation en clair du répertoire personnel, ensemble LAN par utilisateur), [gitback](#gitback) (miroirs de dépôt de gestion de versions, par puddle), [lockbox](#lockbox) (synchronisation chiffrée du répertoire personnel, par puddle). Chaque sous-système a ses propres règles de contenu mais réutilise le canal pair, le port et la confiance mTLS du swarm. Le swarm est singulier et à l'échelle de la flotte — voir [fleet](#fleet) pour les distinctions de mots de portée. Voir [Swarm](howto/swarm/set-up-swarm.md).

## trait

Un fait collecté à propos d'un Envoy, rapporté au check-in — OS, matériel, paquets installés, ports en écoute, montages de système de fichiers, modules noyau, etc. Les traits alimentent les expressions `when:`, l'évaluation de conformité, les vues d'inventaire et l'analyse de tendances. Chaque trait a un collecteur dédié dans `agent/src/traits/`. Voir la [référence des traits](reference/vigo/traits/).

## usercrate

Un configcrate de configuration limité aux environnements d'un ou plusieurs utilisateurs (création de compte, fichiers du répertoire personnel, dotfiles, tâches cron personnelles), analogue à un [configcrate](#configcrate) ordinaire mais organisé autour de l'identité d'utilisateur humain et de compte de service. Les usercrates résident dans `stacks/**/usercrates/*.vgo` — à n'importe quel niveau de répertoire — mais le répertoire est une bibliothèque : depuis la version 0.54.0 un usercrate est **inerte jusqu'à inclusion positive** via le champ `usercrates:` d'un porteur (bloc match, common.vgo, rôle, ou bloc par-env de environments.vgo — les quatre mêmes porteurs sur lesquels vit `configcrates:`). Le champ `name:` au niveau du configcrate est optionnel et prend par défaut la racine du nom de fichier (`dan.vgo` → `dan`). Distinct du type de [ressource](#resource) `user:`, qui est une primitive à l'intérieur d'un usercrate ; la règle de forme exige exactement une ressource `type: user`, avec des ressources non-user supplémentaires autorisées (file pour authorized_keys/dotfiles, exec pour la configuration post-création). Voir [Gestion des utilisateurs](reference/configcrate-language.md).

**Utilisation :** déposez `stacks/usercrates/alice.vgo` pour définir alice, puis ajoutez `usercrates: [alice]` là où elle doit s'appliquer — typiquement `stacks/common.vgo` (à l'échelle de la flotte) ou un `common.vgo` par site (limité à cette branche). Les collisions de noms avec des configcrates ordinaires sont fatales au chargement ; usercrates et configcrates partagent un seul espace de noms, et les références `configcrates:` vs `usercrates:` portent sur des ensembles disjoints (une mauvaise utilisation inter-types échoue au chargement avec l'indication du champ corrigé). Les usercrates non référencés émettent un avertissement au moment de la publication.

## secret

Une référence à une valeur stockée dans le coffre-fort de secrets, écrite dans [stocks](#stacks) sous la forme `secret:<path>` (par ex. `password: "secret:vigo/db/dsn"`). Le stockage est chiffré en AES-256-GCM sous la clé maîtresse à `<secrets-dir>/.master.key` ; les backends incluent `local` (par défaut) et `isopass` (HTTP de production). Au chargement de la configuration, le chargeur valide chaque référence `secret:` contre le `secrets.Provider` configuré (les secrets manquants échouent bruyamment) et substitue le marqueur de remplacement `__VIGO_SECRET__<path>__END_VIGO_SECRET__` dans la configuration résolue en mémoire — la valeur en clair ne vit jamais sur l'`EnvoyConfig` et ne franchit jamais le fil de l'agent. Les agents reçoivent des bundles portant le marqueur, récupèrent par lots les valeurs résolues via le RPC `VigoAgent.ResolveSecrets` protégé par signature (le serveur applique l'habilitation par Envoy contre les marqueurs que référence le propre bundle de l'Envoy), substituent sur place, puis appliquent. Le champ `secrets_epoch` de chaque `PolicyBundle` avance à chaque rechargement + rotation détectée par le watcher ; les agents abandonnent leur cache de secrets résolus en mémoire lorsqu'il s'incrémente. Voir [Convention des secrets](../CLAUDE.md#secrets-convention) et [vigocli secrets](reference/vigocli/secrets.md).

## lockbox

Synchronisation du répertoire personnel par utilisateur, toujours chiffrée, sur le substrat [swarm](#swarm). L'appartenance correspond au [puddle](#puddle) de l'utilisateur (ADR-020 — lockbox est réservé au puddle). Les fichiers déposés dans `~/lockbox/` sont chiffrés avec age vers chaque Envoy du puddle de l'utilisateur et synchronisés en tant que texte chiffré — chaque Envoy détient les mêmes octets chiffrés, seuls les membres du puddle peuvent déchiffrer. Nécessite le mot de passe lockbox de l'utilisateur pour le déchiffrement. Le frère chiffrement-d'abord de [longdrawer](#longdrawer) : même transport, règles de contenu différentes. Voir [Lockbox](howto/swarm/set-up-lockbox.md).

## puddle

Primitive d'identité puddle par utilisateur (ADR-014). Une seule paire de clés Ed25519 représentant « la collection d'Envoys de cet utilisateur, traitée comme une unité de confiance unique », enveloppée sous la phrase secrète de l'utilisateur à l'aide de l'enveloppe Argon2id+XChaCha20 de lockbox. Les sous-systèmes frères ([gitback](#gitback) sous ADR-015, l'appartenance lockbox sous ADR-016) la consomment comme ancre de confiance — « les Envoys du puddle X » exprimés comme une seule clé publique plutôt qu'une cérémonie d'invitation par paire. Onboarding via [peer-pair](#peer-pair) ; le scp manuel survit comme repli air-gapped. État à `~/.vigo-puddle/`. CLI : `vigo swarm puddle {init,pair,join,unlock,lock,pubkey,members,rotate-passphrase,status}`. Voir [Puddle](howto/swarm/set-up-puddle.md).

## peer-pair

Le flux d'onboarding principal pour le [puddle](#puddle) et (par ADR-015) le substrat gitback unifié. L'Envoy actif émet un code d'appairage à 8 caractères, à usage unique, valable 5 minutes (`NNNN-NNNN`) ; l'Envoy rejoignant POST le code sur swarm mTLS (port 1531) pour recevoir le blob d'identité enveloppé. La phrase secrète de l'utilisateur ne quitte jamais l'Envoy de destination. Même modèle d'autorisation humaine explicite que l'appairage Signal/Bluetooth ; la double barrière mTLS-CA-de-confiance + code dicté empêche tout autre pair de la CA de flotte de demander silencieusement le blob.

## gitback

Distribution personnelle de gestion de versions pour reprise après sinistre sur le substrat [swarm](#swarm). L'agent analyse `~/gitback/<project>/` à la recherche de dépôts ayant un sidecar `.gitback.yaml`, les empaquette en [bundles](#bundle) git, et les distribue comme dépôts nus appartenant à root sur les Envoys pairs. Pur P2P au-dessus du swarm — aucune implication du serveur dans la sélection ou la livraison. Default-deny par Envoy depuis la 0.41.0+ ; les opérateurs autorisent les hôtes via la liste de motifs `swarm.gitback.enabled` dans server.yaml (premier match gagnant, le préfixe `-` refuse). L'opt-in par utilisateur est conditionné par le flag `gitback: true` sur la ressource user dans l'usercrate. Voir [Gitback](howto/swarm/set-up-gitback.md).

## longdrawer

Synchronisation du répertoire personnel par utilisateur sur le substrat [swarm](#swarm). Les fichiers de `~/longdrawer/` se répliquent vers les Envoys pairs sur le canal HTTPS pair mTLS du swarm, avec le groupe multicast LAN propre à longdrawer sur `224.0.0.43:1532` pour la découverte. Contrairement à [lockbox](#lockbox), longdrawer n'est *pas* limité au puddle — l'ensemble de synchronisation comprend les Envoys du segment LAN de l'utilisateur (plus tout `cross_subnet_peers:`) qui détiennent un répertoire `~/longdrawer/` pour cet utilisateur. En clair au repos (le chiffrement a été déplacé vers [lockbox](#lockbox) depuis la 0.26.1). Pairs inter-sous-réseaux via `cross_subnet_peers:` dans `~/.longdrawer/config.yaml`. Voir [Longdrawer](howto/swarm/set-up-longdrawer.md).

## audit bundle

Un zip autonome qu'un opérateur Vigo peut remettre directement à un auditeur externe. Généré par cadre via [`GET /api/v1/report/audit-bundle/{slug}`](reference/api.md#audit-bundle-export) ou `vigocli report audit-bundle <framework>`. Contient `manifest.json` (SHA-256 de chaque autre artefact), `report.pdf` (rendu WeasyPrint) + `report.html` (repli lisible par l'auditeur), `evidence.json` (structuré par contrôle : portée, statut, configcrates, documents joints), `docs/<id>__<filename>` (copies déchiffrées de chaque document de conformité téléversé par l'opérateur lié aux contrôles du cadre), `audit-log.csv` (entrées de la chaîne d'audit dans la fenêtre rétrospective), et `convergence.csv` (convergence quotidienne de la flotte dans la même fenêtre). La période par défaut est de 365 jours ; configurable par requête. Vérifiez l'intégrité après extraction avec `jq -r '.artifacts[] | "\(.sha256)  \(.path)"' manifest.json | sha256sum -c`. Livré en 0.32.5.

## scope

Une classification par contrôle sur chaque entrée de catalogue de cadre qui capture qui est responsable de sa satisfaction. Livrée en 0.32.3 en même temps que la complétion du catalogue en 0.32.2.

- **enforced** — les configcrates Vigo appliquent le contrôle technique (règles de pare-feu, état des paquets, mode de fichier, sysctl, gestion des utilisateurs, configuration SSH, profils AppArmor, etc.). La couverture bascule lorsque les configcrates d'un Envoy portent l'étiquette `compliance:` correspondante.
- **attested** — Vigo collecte des preuves mais n'applique pas. Détection de dérive, trait [security_scan](reference/vigo/traits/), intégrité de la chaîne d'audit, vérifications de flotte [Connwaer](#connwaer). La couverture nécessite que le signal pertinent soit présent et sain.
- **documented** — l'opérateur satisfait via un artefact téléversé (BAA, plan IR, évaluation des risques, dossiers de formation, contrats signés). La couverture bascule lorsqu'au moins un [document de conformité](#compliance-docs) actif est lié au couple (framework, control_id). Sans document joint, les contrôles de portée documented s'affichent comme « manquant — nécessite une documentation téléversée ».
- **not_applicable** — le contrôle est hors de la portée du déploiement (par ex. la fonction HIPAA « Isolating Health Care Clearinghouse Functions » pour une entité non-clearinghouse). Exclu du dénominateur du cadre.

Exposée sous forme de colonne Scope sur les pages de détail de conformité par Envoy, de sorte qu'un contrôle « manquant » indique à l'opérateur s'il faut assigner un configcrate ou téléverser un document. Voir [Compliance](reference/compliance-matrix.md).

## compliance role

Le troisième rôle de l'interface web ajouté en 0.34.2, situé entre admin et viewer. Permissions par défaut : `compliance.read` + `compliance.docs.write` uniquement — chaque page de cadre est visible, la documentation justificative peut être téléversée, et l'export d'audit bundle par cadre se télécharge (il est classé `compliance.read` car le bundle est un artefact de preuve, non le flux brut du journal d'audit). Le rôle exclut délibérément `audit.read`, `fleet.read`, et toute permission de mutation, de sorte qu'un titulaire ne peut pas voir les Envoys/configcrates/configuration/secrets/scrier/payload, ne peut pas publier ni dispatcher de tâches, et ne peut pas lire directement le journal d'audit à chaîne de hachage. Destiné au personnel GRC et aux téléverseurs de documents de conformité, **pas** aux auditeurs — les auditeurs devraient se voir attribuer `admin` ou `viewer` selon la profondeur d'accès aux preuves requise. Stocké sous `role: 'compliance'` dans `users.role` ; la contrainte CHECK de la base énumère les trois valeurs valides. Voir [Authentification](howto/operate/set-up-auth.md#roles-and-permissions).

## compliance docs

Le magasin d'artefacts téléversés par l'opérateur qui sous-tend les contrôles de cadre de portée `documented` — BAA, plans de réponse aux incidents, évaluations des risques, dossiers de formation, contrats tiers signés, etc. Livré en 0.32.4. Les fichiers atterrissent chiffrés par enveloppe au repos sous `compliance.docs.docs_dir` (par défaut `/srv/vigo/compliance-docs/`), enveloppés avec la même clé maîtresse AES-256-GCM qu'utilise le fournisseur de secrets. Les métadonnées vivent dans la table `compliance_docs` ; les pièces jointes par contrôle vivent dans `compliance_doc_links` de sorte qu'un seul BAA téléversé peut satisfaire HIPAA 164.308(b), HITRUST 09.f, SOC 2 CC9.2 en un seul téléversement.

La validation MIME par octets magiques rejette les types de fichiers renommés (par ex. un JPEG se faisant passer pour un `.pdf`). Liste d'autorisation par défaut : PDF, DOCX, XLSX, PPTX, TXT, MD, PNG, JPG, SVG. Plafonds de taille configurables : 50 Mio par document, 5 Gio par cadre, 50 Gio au total — surchargeables dans `compliance.docs.{max_doc_bytes, max_per_framework_bytes, max_total_bytes}`.

La suppression douce est le comportement par défaut — le document disparaît des listes mais le texte chiffré reste sur disque afin que les [audit bundles](reference/api.md#compliance-documentation) historiques puissent encore le vérifier. La purge physique nécessite la permission dédiée `compliance.docs.purge`. Chaque téléversement, remplacement, suppression douce, purge, liaison, déliaison et téléchargement écrit une entrée à chaîne d'audit infalsifiable avec le SHA-256 du document.

Les opérateurs interagissent via le panneau « Supporting Documentation » dans le drilldown par contrôle de la page Compliance Evidence, ou via [`POST /api/v1/compliance/docs`](reference/api.md#compliance-documentation) pour l'outillage.

Disponible uniquement sur le backend de secrets `local` aujourd'hui ; sous tout autre backend les routes renvoient `503` et les contrôles de portée documented restent manquants.

## waiver

Une exception de conformité par Envoy — « accepter ce contrôle comme satisfait malgré la non-application ». Les waivers vivent dans `stacks/**/waivers.vgo` (arborescence unique depuis la 0.33.1) et héritent à travers la chaîne de répertoires : la racine s'applique à l'échelle de la flotte, les waivers de sous-répertoire ne s'appliquent qu'aux Envoys sous cette sous-arborescence. Chaque waiver porte un identifiant `control`, une `reason`, un `approved_by`, et une date `expires:` optionnelle. Les contrôles waivés comptent comme « accepted » dans le scoring de conformité et s'affichent avec un badge « waived » distinct couleur Lavender sur les tableaux de bord — non masqués. Distinct de la [revendication de conformité](#compliance), qui déclare ce qu'un configcrate satisfait ; un waiver déclare ce dont un Envoy est dispensé. Voir [Waivers + revendications de conformité](reference/configcrate-language.md#compliance-waivers--claims).

## stacks

Le répertoire parent pour toute l'opération de configuration Vigo, à `/srv/vigo/stacks/`. Contient trois sous-arborescences frères : [stacks](#stacks) (l'arborescence de travail éditée par l'opérateur), [.live](#live) (sortie publiée validée, ce que le serveur lit), et [examples](#examples) (templates de référence en lecture seule semés par l'image). Les opérateurs éditent stacks et exécutent `vigocli config publish` pour synchroniser vers .live. Livré en 0.33.1, remplaçant la disposition antérieure `worksite/{stacks,scaffolding,structure}/` — l'ancienne arborescence `scaffolding/` a été intégrée dans stacks, `structure/` renommée en `.live/`. Les installations existantes migrent automatiquement au premier démarrage.

## examples

L'arborescence de templates d'installation en lecture seule, un frère de [stacks](#stacks) à `/srv/vigo/stacks-examples/`. Semée depuis l'image au démarrage du conteneur avec des templates `*.vgo.example` que l'opérateur peut adapter. Matérialisée dans [stacks](#stacks) via `vigocli config examples copy <name>`, qui retire le suffixe `.example` et préserve le sous-chemin source. Jamais lue par le serveur, jamais publiée — c'est un catalogue de pièces.

## live

L'arborescence de sortie publiée validée sous [stacks](#stacks), à `/srv/vigo/.live/`. `vigocli config publish` lint [stacks](#stacks) et synchronise atomiquement les octets canoniques ici ; le serveur lit `.live/` à chaque rechargement. Verrouillée en lecture seule entre les publications. Ne jamais éditer à la main — le validateur de nom de fichier du chargeur refusera de recharger sur un fichier mal placé. Renommée de `structure/` (0.28.0) à nouveau en `.live/` en 0.33.1.

---
*Confidentiel -- Alexander4, LLC. Ne pas redistribuer. Voir [licence](legal/license.md).*
