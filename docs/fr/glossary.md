<!-- vigo-i18n source_sha:c462d2e59fb0411b8cd14e07a643026684035e3cfd3b6b7cfc68c110158c37aa -->

# Glossaire

Définitions canoniques des termes propres à Vigo. Par ordre alphabétique. Chaque entrée est un paragraphe avec des liens croisés vers la page qui couvre le concept en profondeur.

Si vous rencontrez dans la documentation ou la CLI un terme qui ne figure pas ici, c'est un bug de documentation — merci de le signaler.

## blob

Une séquence d'octets opaque distribuée par le sous-système [swarm](howto/swarm/set-up-swarm.md), adressée par son hash SHA-256. Un blob est immuable : redistribuer un contenu mis à jour signifie semer un nouveau blob et révoquer l'ancien. Les agents stockent les blobs dans `/var/lib/vigo/swarm/.blobs/` et matérialisent vers eux des liens symboliques sous `/var/lib/vigo/swarm/files/` selon les chemins cibles du manifest.

## bundle

Une archive de dépôt git autonome produite par `git bundle create`, utilisée comme unité de distribution de [gitback](howto/swarm/set-up-gitback.md). Les bundles peuvent être complets (historique intégral du dépôt) ou incrémentaux (deltas depuis un bundle précédent). Les destinataires matérialisent les bundles en dépôts bare à `/var/lib/vigo/swarm/gitback/mirrors/<seeder>/<user>/<project>.git`.

## compliance

Couverture des cadres de normes. Se divise en deux surfaces, tenues séparées depuis la 0.32.0 :

- **Conformité Envoy** — si les configcrate d'un nœud géré satisfont les contrôles de cadres réglementaires (HIPAA, SOC 2, PCI DSS, NIST 800-53, ISO 27001, etc.). Mesurée en pourcentage de couverture par cadre. Un contrôle compte comme couvert si et seulement si (a) l'Envoy possède une configcrate taguée avec ce contrôle via `compliance:`, (b) une vérification de trait [Connwaer](#connwaer) sur l'Envoy l'escalade, ou (c) une [dérogation](#waiver) s'applique. C'est la surface que mesurent les rapports de cadre sous `/api/v1/report/<slug>`.
- **Conformité produit Vigo** — ce que le serveur et l'agent Vigo satisfont par construction (mTLS, RBAC, chaîne de hash d'audit, chiffrement des secrets, etc.). Réside dans `server/compliance/AppControls` et est exposée séparément à `/platform/compliance` (Platform → Data). Utile pour les questionnaires fournisseurs ; jamais intégrée au scoring des cadres.

À distinguer de la [convergence](#convergence) : la convergence dit « mes configcrate se sont-elles appliquées avec succès », la conformité dit « mes configcrate satisfont-elles le cadre X ». Voir [scope](#scope) pour la façon dont chaque contrôle de cadre est classifié, et [Compliance](reference/compliance-matrix.md) pour la vue d'ensemble complète.

## connwaer

Un agent Rust autonome pour la vérification active de conformité — vérifiant des choses que Vigo ne peut pas prouver par la seule application (stockage WORM, validation RNG, chiffrement au repos, segmentation réseau). S'exécute aux côtés de l'agent vigo sur les Envoys enrôlés. Produit séparé avec son propre fichier de licence et un comptage de nœuds indépendant. Voir la [documentation Connwaer](reference/compliance-matrix.md).

## convergence

État de convergence des configcrate pour un Envoy, évalué sur deux axes orthogonaux :

- **Axe d'échec** — `converged`, `degraded`, `failed`, `no_data`. Mutuellement exclusifs ; somment au total de la flotte. Tout échec non nul franchit le seuil ; zéro échec donne `converged` quel que soit le nombre de configcrate ayant effectué des modifications.
- **Axe de dérive** — `none`, `changed`, `diverged`. `changed` se déclenche quand la dernière exécution a `ChangedCount > 0` ; `diverged` se déclenche après `DivergedThreshold` exécutions consécutives avec modifications (par défaut 5). Diverged est un sous-ensemble strict de changed. Indépendant de l'axe d'échec — un Envoy peut être `converged` ET `diverged` simultanément (zéro échec, mais churn persistant des configcrate).

L'accessibilité (en ligne / hors ligne) est un troisième axe orthogonal suivi par `server/freshness`. Voir le [guide de convergence](reference/configcrate-language.md).

## drift

L'axe de dérive de la convergence (ci-dessus). Calculé par `convergence.ComputeDrift` sur l'historique d'exécutions récentes d'un Envoy, exprimé en `DriftLevel` (`none` / `changed` / `diverged`). La dérive concerne purement le fait de *savoir si les configcrate continuent de muter* — elle ne dit rien sur le succès des mutations. Une configcrate non idempotente qui recrée le même fichier à chaque exécution produit des résultats `Created` persistants et place l'Envoy en `diverged` même si techniquement chaque exécution réussit.

## envoy

Un nœud géré exécutant l'agent Rust `vigo`. Chaque Envoy s'enrôle une fois, reçoit un UUID client, et fait un check-in périodique pour tirer l'état désiré et rapporter les résultats. Le serveur central adresse les Envoys individuels par UUID ; les opérateurs les référencent par hostname. Le chemin retrait/remplacement/élimination utilise trois verbes différents (revoke, puddle evict, delete) avec des portées délibérément différentes — voir la référence [Cycle de vie d'un Envoy](howto/operate/troubleshoot.md).

## executor

L'implémentation Rust d'un type de ressource dans l'agent (`file`, `package`, `service`, `user`, `cron`, etc.). Les executors vérifient l'état courant, n'agissent que sur la dérive (idempotents), et renvoient l'un de `None`/`Created`/`Updated`/`Deleted`/`Skipped`/`Blocked`. Voir la [référence des executors](reference/vigo/resources/).

## fleet

Tous les [Envoy](#envoy) enrôlés auprès d'un serveur donné. « Fleet-wide » signifie *à travers tous ces Envoys* — c'est la portée la plus large dans Vigo. À distinguer de deux portées plus étroites que l'UI ne doit pas confondre avec elle :

- Le **[swarm](#swarm)** est le substrat P2P connectant les Envoys entre eux ; en pratique le swarm couvre les mêmes Envoys que la flotte, mais c'est la *couche de transport*, pas l'ensemble des machines. Il y a exactement un seul swarm — il n'existe pas de « swarm d'un utilisateur ».
- Une **[puddle](#puddle)** est une unité de confiance *par utilisateur* : la collection d'Envoys d'un seul utilisateur, traitée comme une seule identité Ed25519. « La puddle d'un utilisateur » a un sens ; « la puddle de la flotte » non.

Mots de portée canoniques, du plus étroit au plus large : **puddle** ⊆ Envoys-exécutant-un-utilisateur-donné ⊆ **fleet** ; le **swarm** est le substrat au-dessus de la flotte. (« Mesh » n'est *pas* un terme canonique de Vigo — là où une prose plus ancienne disait « mesh » elle désignait soit la puddle (pour [lockbox](#lockbox)) soit un ensemble de sync ad-hoc par utilisateur (pour [longdrawer](#longdrawer)) ; utilisez le terme précis.)

Voir le [diagramme de hiérarchie de portée](howto/swarm/set-up-swarm.md#scope-fleet-vs-swarm-vs-puddle) sur la page Swarm pour la relation entre ces couches et les quatre sous-systèmes de contenu.

## filecast

Distribution de fichiers poussée par l'administrateur au-dessus du substrat [swarm](#swarm). Un opérateur exécute `vigocli swarm filecast distribute <file>` (ou dépose un fichier + un sidecar `.swarm` sur un seeder) et le blob se réplique à travers la flotte via le canal pair du swarm. Filecast est l'un des quatre sous-systèmes de contenu qui chevauchent le swarm ; les trois autres ([longdrawer](#longdrawer), [gitback](#gitback), [lockbox](#lockbox)) sont à portée utilisateur, tandis que filecast est à portée opérateur et fleet-wide. Exposé dans l'UI web à [`/swarm/filecast`](howto/).

## manifest

Le magasin de métadonnées LWW-CRDT pour les blobs du swarm, propagé par gossip entre les Envoys. Suit quels blobs existent, leurs cibles, leurs politiques de rétention, et les pierres tombales de révocation. Chaque Envoy détient un manifest local ; les écarts de version durant le multicast déclenchent une synchro du manifest sur le canal HTTPS pair. Voir [Manifest du Swarm](howto/swarm/set-up-swarm.md#manifest).

## hostcrate

Tout fichier `.vgo` contenant un bloc `envoys:` avec des entrées `- match:`. Mappe des patterns/globs de hostname vers des rôles, des configcrate, et des vars. Premier match gagnant. Conventionnellement nommé `envoys.vgo` mais le loader parcourt tous les fichiers `.vgo`, donc des noms de fichiers par hôte (`plex.vgo`, `web01.vgo`) fonctionnent aussi. Voir [Config multi-axes](reference/configcrate-language.md).

## configcrate

Un ensemble de ressources réutilisable dans `stacks/configcrates/*.vgo`. Déclare des ressources, des vars par défaut, et des revendications de conformité optionnelles. Composée en [rôles](#role), appliquée aux [Envoys](#envoy) via les [hostcrate](#hostcrate) ou l'héritage de `common.vgo` au niveau répertoire. Les configcrate sont l'unité de composition primaire pour la config à l'échelle d'un Envoy. Voir [Format de config](reference/configcrate-language.md) et [Langage de ressources](reference/configcrate-language.md).

## resource

Une déclaration d'état idempotente unique à l'intérieur d'une configcrate — un fichier, un package, un service, un compte utilisateur, etc. Les ressources déclarent un `type:` (mappé à un [executor](#executor)), des paramètres, et des attributs optionnels `depends_on` / `notify` / `when:`. Voir [Langage de ressources](reference/configcrate-language.md).

## retract

Un inverse auto-généré d'une configcrate — les ressources de type fichier basculées en `state: absent`, les services en `stopped`+`enabled: false`, les packages en `absent`, etc. Utilisé pour une suppression propre de configcrate. Généré par `vigocli config publish` quand la rétraction est activée ; le fichier `.retract` résultant est inerte jusqu'à ce qu'il soit renommé et appliqué. Voir [Rétraction de configcrate](reference/configcrate-language.md).

## role

Un regroupement nommé de configcrate et de vars défini dans `roles.vgo` (ou le répertoire `roles/` hérité). Supporte `when:` (conditionnel à l'échelle du rôle), `case:` (groupes de configcrate par plateforme), et `includes:` (composition de rôle à un seul niveau). Assigné aux Envoys via les [hostcrate](#hostcrate). Voir [Format de config](reference/configcrate-language.md).

## sandgorgon

Le sous-système de cycle de vie bare metal de Vigo : provisioning PXE, intégration BMC (Redfish), workflow de mise hors service avec génération de certificat NIST 800-88, import d'inventaire CSV/NetBox. Exposé à `/sandgorgon/` dans l'UI web et via `vigocli sandgorgon`. **Différé à post-v1** -- le code est présent mais le cycle de vie bare metal n'est pas une fonctionnalité supportée en v1. Voir [Sandgorgon](overview.md).

## scrier

Le sous-système d'accès distant admin de Vigo : sessions interactives SSH, RDP, et VNC vers n'importe quel Envoy enrôlé, routées via l'`AgentStream` sortant de l'Envoy + un `TunnelStream` dédié. Aucun port entrant côté Envoy — la proposition de valeur clé MSP/pare-feu-client. Accès navigateur à `/scrier/{envoy_id}` ; accès CLI via `vigocli scrier ssh`. Voir [Scrier](howto/operate/set-up-scrier.md).

## seed

L'instance d'origine d'un blob de swarm sur un Envoy — la source de vérité pour ce blob. Aujourd'hui les seeds sont créés exclusivement par `vigocli swarm filecast distribute`, qui écrit dans le répertoire de seed monté en bind du serveur. Le verbe de seed originé par l'Envoy (`vigo swarm seed add`) est différé — voir `pending.md` pour le statut. Les pairs tirent les chunks depuis les seeds jusqu'à disposer de copies locales complètes.

## spanner

L'ensemble du plan de contrôle fédéré — une constellation de serveurs [bolt](#bolt) coopérant comme des pairs à droits d'écriture égaux pour opérer une seule flotte. La CLI (`vigocli spanner`), la route UI (`/spanner`), l'espace de noms REST (`/api/v1/spanner/*`), la clé YAML (`spanner:`), et le préfixe de métrique (`vigo_spanner_*`) parlent tous le vocabulaire spanner. Il n'y a ni hub ni primaire : chaque bolt exécute un code identique, détient le roster d'admissions complet (répliqué en CRDT via gossip de compteurs multicast + un pull de snapshot REST), et répond aux requêtes admin à l'échelle de la flotte en agrégeant depuis ce roster (ADR-026). L'appartenance s'établit avec `vigocli spanner init` / `bolt invite` / `bolt join` ; le loader échoue durement sur les clés hub-spoke retirées (`role`, `bolt_id`, `hub_addr`, …) avec des pointeurs de migration. Voir [Configurer Spanner](howto/operate/set-up-spanner.md).

## bolt

Un `vigosrv` participant à une fédération [spanner](#spanner) comme pair à droits d'écriture égaux. Possède une partition par pattern de hostname de la flotte (`spanner.patterns`, premier match gagnant, chevauchement refusé au publish + au boot) ; gère les check-ins d'agent pour ses Envoys ; détient le roster d'admissions complet et agrège les lectures à l'échelle de la flotte depuis celui-ci (pas de hub). Configuré avec `spanner.mode: spanner` plus un `spanner_id` et des `patterns` — les valeurs de mode retirées `founder`/`joiner` échouent durement (chaque bolt est un `spanner` pair à droits égaux). L'identité par bolt est la clé publique Ed25519 générée à `server/bolt/`, pas un ID configuré en YAML.

## stacks

L'arbre unique édité par l'opérateur sous [stacks](#stacks), à `/srv/vigo/stacks/`. Contient toutes les primitives de config : [configcrates](#stone) (définitions de configcrate), [usercrates](#usercrate), `templates/` (fichiers de template Go référencés par `source:`), `tasks/`, `workflows/`, plus les fichiers d'assignation `envoys.vgo` (hostcrate), `roles.vgo`, `common.vgo`, `waivers.vgo`, `environments.vgo`, et les fichiers d'héritage de revendications `compliance.vgo`. Le regroupement de la 0.33.1 a fusionné le sous-arbre `stacks/` antérieur dans stacks — il n'y a plus qu'un seul arbre désormais, et `vigocli config publish` ne synchronise que celui-ci vers [.live](#live).

## stone

Un fichier de définition de configcrate dans `stacks/configcrates/**/*.vgo`. Le nom de répertoire `configcrates/` est la métaphore de construction au niveau du système de fichiers de ce que le YAML appelle une [configcrate](#configcrate) — la clé YAML est `configcrates:`, et l'identifiant Go/Rust est `ConfigcrateDefinition`. Les opérateurs écrivant `configcrates: [nginx]` dans un rôle référencent le stone à `stacks/configcrates/nginx.vgo`.

## swarm

Le substrat réseau P2P réservé aux Envoys. Le swarm est la couche de transport sur laquelle chevauchent tous les sous-systèmes de contenu P2P : découverte de pairs (multicast sur `224.0.0.42:1531` + indices issus du registre), HTTPS mTLS entre Envoys sur le port 1531, gossip CRDT de manifest, et chunking adressé par contenu. Le swarm ne définit pas lui-même *ce qui* est distribué — ce sont les quatre sous-systèmes de contenu qui le font : [filecast](#filecast) (blobs poussés par l'admin, fleet-wide), [longdrawer](#longdrawer) (sync de home en clair, ensemble LAN par utilisateur), [gitback](#gitback) (miroirs de dépôt de gestion de sources, par puddle), [lockbox](#lockbox) (sync de home chiffrée, par puddle). Chaque sous-système a ses propres règles de contenu mais réutilise le canal pair, le port, et la confiance mTLS du swarm. Le swarm est singulier et fleet-wide — voir [fleet](#fleet) pour les distinctions de mots de portée. Voir [Swarm](howto/swarm/set-up-swarm.md).

## trait

Un fait collecté à propos d'un Envoy, rapporté au check-in — OS, matériel, packages installés, ports en écoute, montages de système de fichiers, modules noyau, etc. Les traits alimentent les expressions `when:`, l'évaluation de conformité, les vues d'inventaire, et l'analyse de tendances. Chaque trait a un collecteur dédié dans `agent/src/traits/`. Voir la [référence des traits](reference/vigo/traits/).

## usercrate

Une configcrate de config limitée aux environnements d'un ou plusieurs utilisateurs (création de compte, fichiers de home, dotfiles, tâches cron personnelles), analogue à une [configcrate](#configcrate) classique mais organisée autour de l'identité humaine et de compte de service. Les usercrate vivent dans `stacks/**/usercrates/*.vgo` — à n'importe quel niveau de répertoire — mais le répertoire est une bibliothèque : depuis la 0.54.0 une usercrate est **inerte jusqu'à inclusion positive** via le champ `usercrates:` d'un porteur (bloc match, common.vgo, rôle, ou bloc par environnement d'environments.vgo — les mêmes quatre porteurs sur lesquels vit `configcrates:`). Le champ `name:` au niveau configcrate est optionnel et prend par défaut la racine du nom de fichier (`dan.vgo` → `dan`). À distinguer du type de [ressource](#resource) `user:`, qui est une primitive à l'intérieur d'une usercrate ; la règle de forme exige exactement une ressource `type: user`, des ressources non-user additionnelles (file pour authorized_keys/dotfiles, exec pour la configuration post-création) étant permises. Voir [Gestion des utilisateurs](reference/configcrate-language.md).

**Usage :** déposez `stacks/usercrates/alice.vgo` pour définir alice, puis ajoutez `usercrates: [alice]` là où elle doit s'appliquer — typiquement `stacks/common.vgo` (fleet-wide) ou un `common.vgo` par site (limité à cette branche). Les collisions de noms avec des configcrate classiques sont fatales au chargement ; usercrate et configcrate partagent un seul espace de noms, et `configcrates:` vs `usercrates:` référencent des ensembles disjoints (un mauvais usage inter-types échoue au chargement avec l'indice de champ corrigé). Les usercrate non référencées émettent un avertissement au moment du publish.

## secret

Une référence à une valeur stockée dans le coffre de secrets, écrite dans [stocks](#stacks) comme `secret:<path>` (p. ex. `password: "secret:vigo/db/dsn"`). Le stockage est chiffré en AES-256-GCM sous la clé maître à `<secrets-dir>/.master.key` ; les backends incluent `local` (par défaut) et `isopass` (HTTP de production). Au chargement de config, le loader valide chaque réf `secret:` contre le `secrets.Provider` configuré (les secrets manquants échouent bruyamment) et substitue le marqueur de placeholder `__VIGO_SECRET__<path>__END_VIGO_SECRET__` dans la config résolue en mémoire — la valeur en clair ne vit jamais sur l'`EnvoyConfig` et ne traverse jamais le fil de l'agent. Les agents reçoivent des bundles portant le placeholder, récupèrent par lots les valeurs résolues via le RPC `VigoAgent.ResolveSecrets` protégé par signature (le serveur applique le droit par Envoy contre les placeholders que le propre bundle de l'Envoy référence), substituent en place, puis appliquent. Le champ `secrets_epoch` sur chaque `PolicyBundle` avance à chaque rechargement + rotation détectée par le watcher ; les agents abandonnent leur cache de secrets résolus en mémoire lorsqu'il s'incrémente. Voir [Convention de secrets](../CLAUDE.md#secrets-convention) et [vigocli secrets](reference/vigocli/secrets.md).

## lockbox

Sync de répertoire home toujours chiffrée par utilisateur sur le substrat [swarm](#swarm). L'appartenance est la [puddle](#puddle) de l'utilisateur (ADR-020 — lockbox est puddle-only). Les fichiers déposés dans `~/lockbox/` sont chiffrés avec age vers chaque Envoy de la puddle de l'utilisateur et synchronisés en tant que texte chiffré — chaque Envoy détient les mêmes octets chiffrés, seuls les membres de la puddle peuvent déchiffrer. Nécessite le mot de passe lockbox de l'utilisateur pour le déchiffrement. Le frère du chiffrement-d'abord de [longdrawer](#longdrawer) : même transport, règles de contenu différentes. Voir [Lockbox](howto/swarm/set-up-lockbox.md).

## puddle

Primitive d'identité de puddle par utilisateur (ADR-014). Une seule paire de clés Ed25519 représentant « la collection d'Envoys de cet utilisateur, traitée comme une seule unité de confiance », enveloppée sous la passphrase de l'utilisateur via l'enveloppe Argon2id+XChaCha20 de lockbox. Les sous-systèmes frères ([gitback](#gitback) sous ADR-015, l'appartenance lockbox sous ADR-016) la consomment comme ancre de confiance — « les Envoys de la puddle X » exprimés comme une seule clé publique plutôt qu'une cérémonie d'invitation par paire. Onboarding via [peer-pair](#peer-pair) ; le scp manuel survit comme repli air-gapped. État à `~/.vigo-puddle/`. CLI : `vigo swarm puddle {init,pair,join,unlock,lock,pubkey,members,rotate-passphrase,status}`. Voir [Puddle](howto/swarm/set-up-puddle.md).

## peer-pair

Le flux d'onboarding primaire pour [puddle](#puddle) et (par ADR-015) le substrat gitback unifié. L'Envoy actif émet un code d'appairage à usage unique de 8 caractères valable 5 minutes (`NNNN-NNNN`) ; l'Envoy rejoignant POSTe le code sur mTLS swarm (port 1531) pour recevoir le blob d'identité enveloppé. La passphrase de l'utilisateur ne quitte jamais l'Envoy de destination. Même modèle d'autorisation explicite par l'humain que l'appairage Signal/Bluetooth ; la double barrière mTLS-CA-de-confiance + code dicté empêche tout autre pair de la même CA de flotte de demander silencieusement le blob.

## gitback

Distribution de gestion de sources personnelle pour reprise après sinistre sur le substrat [swarm](#swarm). L'agent scanne `~/gitback/<project>/` à la recherche de dépôts avec un sidecar `.gitback.yaml`, les empaquette en [bundles](#bundle) git, et les distribue comme dépôts bare propriété de root sur les Envoys pairs. P2P pur au-dessus du swarm — aucune implication du serveur dans la sélection ou la livraison. Default-deny par Envoy en 0.41.0+ ; les opérateurs autorisent les hôtes via la liste de patterns `swarm.gitback.enabled` dans server.yaml (premier match gagnant, le préfixe `-` refuse). L'opt-in par utilisateur est protégé par le flag `gitback: true` sur la ressource user dans l'usercrate. Voir [Gitback](howto/swarm/set-up-gitback.md).

## longdrawer

Sync de répertoire home par utilisateur sur le substrat [swarm](#swarm). Les fichiers dans `~/longdrawer/` se répliquent vers les Envoys pairs sur le canal HTTPS pair mTLS du swarm, avec le propre groupe multicast LAN de longdrawer sur `224.0.0.43:1532` pour la découverte. Contrairement à [lockbox](#lockbox), longdrawer n'est *pas* à portée puddle — l'ensemble de sync est constitué des Envoys du segment LAN de l'utilisateur (plus tout `cross_subnet_peers:`) qui détiennent un répertoire `~/longdrawer/` pour cet utilisateur. En clair au repos (le chiffrement a migré vers [lockbox](#lockbox) depuis la 0.26.1). Pairs inter-sous-réseaux via `cross_subnet_peers:` dans `~/.longdrawer/config.yaml`. Voir [Longdrawer](howto/swarm/set-up-longdrawer.md).

## audit bundle

Un zip autonome qu'un opérateur Vigo peut remettre directement à un auditeur externe. Généré par cadre via [`GET /api/v1/report/audit-bundle/{slug}`](reference/api.md#audit-bundle-export) ou `vigocli report audit-bundle <framework>`. Contient `manifest.json` (SHA-256 de chaque autre artefact), `report.pdf` (rendu par WeasyPrint) + `report.html` (repli lisible par l'auditeur), `evidence.json` (structuré par contrôle : portée, statut, configcrate, documents joints), `docs/<id>__<filename>` (copies déchiffrées de chaque document de conformité téléversé par l'opérateur lié aux contrôles du cadre), `audit-log.csv` (entrées de la chaîne d'audit dans la fenêtre de rétrospection), et `convergence.csv` (convergence quotidienne de la flotte dans la même fenêtre). La période par défaut est de 365 jours ; configurable par requête. Vérifiez l'intégrité après extraction avec `jq -r '.artifacts[] | "\(.sha256)  \(.path)"' manifest.json | sha256sum -c`. Livré en 0.32.5.

## scope

Une classification par contrôle sur chaque entrée de catalogue de cadre qui capture qui est responsable de le satisfaire. Livré en 0.32.3 aux côtés de l'achèvement du catalogue en 0.32.2.

- **enforced** — les configcrate Vigo appliquent le contrôle technique (règles de pare-feu, état de package, mode de fichier, sysctl, gestion d'utilisateurs, config SSH, profils AppArmor, etc.). La couverture bascule quand les configcrate d'un Envoy portent le tag `compliance:` correspondant.
- **attested** — Vigo collecte des preuves mais n'applique pas. Détection de dérive, trait [security_scan](reference/vigo/traits/), intégrité de la chaîne d'audit, vérifications de flotte [Connwaer](#connwaer). La couverture exige que le signal pertinent soit présent et sain.
- **documented** — l'opérateur satisfait via un artefact téléversé (BAA, plan IR, évaluation de risque, registres de formation, contrats signés). La couverture bascule quand au moins un [document de conformité](#compliance-docs) actif est lié au couple (cadre, control_id). Sans document joint, les contrôles à portée documented apparaissent comme « manquant — nécessite une documentation téléversée ».
- **not_applicable** — le contrôle est hors de la portée de déploiement (p. ex. « Isolating Health Care Clearinghouse Functions » HIPAA pour un non-clearinghouse). Exclu du dénominateur du cadre.

Exposé comme la colonne Scope sur les pages de détail de conformité par Envoy, de sorte qu'un contrôle « manquant » indique à l'opérateur s'il faut assigner une configcrate ou téléverser un document. Voir [Compliance](reference/compliance-matrix.md).

## compliance role

Le troisième rôle de l'UI web ajouté en 0.34.2, situé entre admin et viewer. Permissions par défaut : `compliance.read` + `compliance.docs.write` seulement — chaque page de cadre est visible, la documentation justificative peut être téléversée, et l'export d'audit-bundle par cadre se télécharge (classé comme `compliance.read` car le bundle est un artefact de preuve, pas le flux brut du journal d'audit). Le rôle exclut délibérément `audit.read`, `fleet.read`, et toute permission de mutation, de sorte qu'un détenteur ne peut pas voir les Envoys/configcrate/config/secrets/scrier/payload, ne peut pas publier ni dispatcher de tâches, et ne peut pas lire directement le journal d'audit chaîné par hash. Destiné au personnel GRC et aux téléverseurs de documents de conformité, **pas** aux auditeurs — les auditeurs devraient se voir assigner `admin` ou `viewer` selon la profondeur d'accès aux preuves requise. Stocké comme `role: 'compliance'` dans `users.role` ; la contrainte CHECK de la BD énumère les trois valeurs valides. Voir [Authentification](howto/operate/set-up-auth.md#roles-and-permissions).

## compliance docs

Le magasin d'artefacts téléversés par l'opérateur qui sous-tend les contrôles de cadre à portée `documented` — BAA, plans de réponse aux incidents, évaluations de risque, registres de formation, contrats tiers signés, etc. Livré en 0.32.4. Les fichiers atterrissent chiffrés par enveloppe au repos sous `compliance.docs.docs_dir` (par défaut `/srv/vigo/compliance-docs/`), enveloppés avec la même clé maître AES-256-GCM qu'utilise le fournisseur de secrets. Les métadonnées vivent dans la table `compliance_docs` ; les pièces jointes par contrôle vivent dans `compliance_doc_links` de sorte qu'un seul BAA téléversé peut satisfaire HIPAA 164.308(b), HITRUST 09.f, SOC 2 CC9.2 en un seul téléversement.

La validation MIME par octets magiques rejette les types de fichiers renommés (p. ex. un JPEG se faisant passer pour un `.pdf`). Liste blanche par défaut : PDF, DOCX, XLSX, PPTX, TXT, MD, PNG, JPG, SVG. Plafonds de taille configurables : 50 Mio par doc, 5 Gio par cadre, 50 Gio au total — surchargeables dans `compliance.docs.{max_doc_bytes, max_per_framework_bytes, max_total_bytes}`.

La suppression douce est le défaut — le doc disparaît des listes mais le texte chiffré reste sur disque pour que les [audit bundles](reference/api.md#compliance-documentation) historiques puissent encore le vérifier. La purge physique nécessite la permission dédiée `compliance.docs.purge`. Chaque téléversement, remplacement, suppression douce, purge, liaison, déliaison, et téléchargement écrit une entrée de chaîne d'audit infalsifiable avec le SHA-256 du doc.

Les opérateurs interagissent via le panneau « Supporting Documentation » dans le drilldown par contrôle de la page Compliance Evidence, ou via [`POST /api/v1/compliance/docs`](reference/api.md#compliance-documentation) pour l'outillage.

Disponible uniquement sur le backend de secrets `local` aujourd'hui ; sous tout autre backend les routes renvoient `503` et les contrôles à portée documented restent manquants.

## waiver

Une exception de conformité par Envoy — « accepter ce contrôle comme satisfait malgré la non-application ». Les dérogations vivent dans `stacks/**/waivers.vgo` (arbre unique depuis la 0.33.1) et héritent à travers la chaîne de répertoires : la racine s'applique fleet-wide, les dérogations de sous-répertoire ne s'appliquent qu'aux Envoys sous ce sous-arbre. Chaque dérogation porte un ID `control`, une `reason`, un `approved_by`, et une date `expires:` optionnelle. Les contrôles dérogés comptent comme « acceptés » dans le scoring de conformité et s'affichent avec un badge « waived » Lavender distinct sur les tableaux de bord — pas masqués. À distinguer d'une [revendication de conformité](#compliance), qui déclare ce qu'une configcrate satisfait ; une dérogation déclare ce dont un Envoy est dispensé. Voir [Dérogations + Revendications de conformité](reference/configcrate-language.md#compliance-waivers--claims).

## stacks

Le répertoire parent de toute l'opération de config Vigo, à `/srv/vigo/stacks/`. Contient trois sous-arbres frères : [stacks](#stacks) (l'arbre de travail édité par l'opérateur), [.live](#live) (sortie publiée validée, ce que le serveur lit), et [examples](#examples) (templates de référence en lecture seule seedés par l'image). Les opérateurs éditent stacks et exécutent `vigocli config publish` pour synchroniser vers .live. Livré en 0.33.1, remplaçant la disposition antérieure `worksite/{stacks,scaffolding,structure}/` — l'arbre `scaffolding/` antérieur a été replié dans stacks, `structure/` renommé en `.live/`. Les installations existantes migrent automatiquement au premier boot.

## examples

L'arbre de templates d'installation en lecture seule, un frère de [stacks](#stacks) à `/srv/vigo/stacks-examples/`. Seedé depuis l'image au démarrage du conteneur avec des templates `*.vgo.example` que l'opérateur peut adapter. Matérialisé dans [stacks](#stacks) via `vigocli config examples copy <name>`, qui retire le suffixe `.example` et préserve le sous-chemin source. Jamais lu par le serveur, jamais publié — c'est un catalogue de pièces.

## live

L'arbre de sortie publiée validée sous [stacks](#stacks), à `/srv/vigo/.live/`. `vigocli config publish` linte [stacks](#stacks) et synchronise atomiquement les octets canoniques ici ; le serveur lit `.live/` à chaque rechargement. Verrouillé en lecture seule entre les publishs. Ne jamais éditer à la main — le validateur de noms de fichiers du loader refusera le rechargement sur un fichier mal placé. Renommé de `structure/` (0.28.0) en `.live/` en 0.33.1.
