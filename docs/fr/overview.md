<!-- vigo-i18n source_sha:31c64a3fe3ac64a0b07a6a39c54ec3a305d48080a56fe59d1f474e7647606a9f -->

# Vigo

**Des dizaines de milliers d'Envoys à une cadence d'une seconde. Des centaines de milliers à une cadence détendue. Un seul serveur.**

Vigo est un moteur distribué d'application d'état : des agents installés sur vos nœuds gérés (Envoys) tirent l'état souhaité depuis un serveur central via gRPC/mTLS, appliquent des modifications de ressources idempotentes et renvoient les résultats — à une cadence serrée, à l'échelle d'une flotte, sur du matériel grand public. Quand un seul serveur ne suffit pas, **Spanner** fédère des pairs égaux en écriture et le plafond disparaît.

Mais l'échelle n'en est que la moitié. Vigo est **bien plus que de la gestion de configuration** — il regroupe la plateforme qu'un opérateur doit habituellement assembler à partir d'une demi-douzaine d'outils : application *et* notation de conformité *et* preuves GRC *et* accès distant inversé *et* tissu de contenu pair-à-pair *et* assistant IA *et* haute disponibilité — sous un unique plan de contrôle signé en mTLS. Vigo associe de façon unique une échelle d'élite à cadence serrée avec ce bundle complet.

---

## Ce que fait Vigo

**Application d'état & convergence.** Décrivez l'état souhaité sous forme de **configcrates** — des ensembles de ressources réutilisables couvrant 69 types de ressources idempotentes (`file`, `package`, `service`, `user`, `exec`, `container`, `vm`, dépôts, l'exécuteur `custom` fourni par l'utilisateur, …). Les ressources forment un DAG `depends_on` avec des déclencheurs `notify`/`subscribes` ; les conditions `when:` et le `content:` en Go-template s'adaptent à chaque hôte. L'agent vérifie avant d'agir, s'auto-répare après des défaillances externes et **converge hors ligne** depuis son bundle en cache lorsque le serveur est injoignable. La convergence est classifiée sur deux axes orthogonaux — échec (Converged/Degraded/Failed) × dérive (None/Changed/Diverged) — plus l'accessibilité.

**Configuration multi-axes.** Un seul langage YAML relie les configcrates, les rôles, les hostcrates (motif de nom d'hôte → rôle/vars, la première correspondance l'emporte), les **usercrates** par utilisateur et les surcharges par environnement, avec une chaîne de précédence des vars claire. La source de vérité, ce sont uniquement des fichiers : modifiez les stacks, `vigocli config publish`, le serveur valide et recharge.

**Conformité & GRC.** Couverture par référentiel (HIPAA, SOC 2, PCI DSS, ISO 27001, et plus) : un contrôle est considéré comme couvert lorsqu'un configcrate le revendique (capacités `provides:` ou découpes de référentiel `bundle:`), qu'une vérification active **Connwaer** l'élève à l'état satisfait, ou qu'une dérogation s'applique. Poussez des preuves de réussite/échec par contrôle vers Vanta, Drata, Secureframe, ServiceNow GRC et d'autres selon une planification ; exportez des PDF de cyber-assurance / d'audit. Convergence et conformité ne sont jamais confondues.

**Accès distant inversé (Scrier).** Accès admin SSH / RDP / VNC à n'importe quel Envoy **sans aucun port entrant** sur le nœud — les sessions ressortent en inverse à travers le flux existant de l'agent, atterrissent transitoirement dans le `authorized_keys` du bon utilisateur OS, et n'exposent jamais de mouvement latéral. RDP/VNC passent par un sidecar `guacd` dans le navigateur.

**Swarm pair-à-pair.** Un substrat mTLS partagé porte six sous-systèmes de contenu — **Filecast** (poussée de fichiers par l'admin), **Gitback** (git par-dessus le Swarm), **Longdrawer** (synchronisation LAN en clair), **Lockbox** (synchronisation chiffrée), **Curator** (registre d'artefacts + shims Docker/S3), **Poolq** (file à journal ordonné) — tous ancrés dans l'identité **Puddle** par utilisateur. Binaires distribués à la flotte, images de conteneurs et mises à jour automatiques de l'agent circulent de nœud à nœud au lieu de marteler le serveur.

**Orchestration.** Distribuez des tâches ad-hoc et des requêtes en direct à travers la flotte ; enchaînez-les en workflows avec branchement conditionnel et abandon ; déployez les changements par lots avec contrôle de santé.

**Assistant IA.** Un assistant intégré, **strictement en lecture seule**, répond aux questions sur votre flotte via ~14 outils (traits, exécutions, conformité, risque, CVE, docs), en flux continu, avec le corpus de documentation en contexte.

**Haute disponibilité & PRA.** Exécutez des réplicas pairs avec basculement en une commande ; ou partitionnez la flotte entre des pairs Spanner. SQLite, avec réplication WAL Litestream optionnelle vers S3, maintient l'état durable.

**Sécurité par construction.** Chaque octet est en mTLS ; chaque requête d'agent est signée en ed25519 contre la clé publique stockée ; les jetons d'enrôlement à usage unique sont hachés au repos ; le RBAC couvre admin / viewer / compliance ; une chaîne de hachage SHA-256 rend le journal d'audit inviolable ; le durcissement au démarrage se ferme en cas de signature de binaire invalide ; les secrets se résolvent depuis un backend enfichable et n'apparaissent jamais dans la config, les journaux, la base, le réseau ou les résultats d'exécution.

**Observabilité & intégrations.** Tableaux de bord pour la convergence, la conformité, le risque (notation 0–100), la santé des agents, l'impact des CVE, la surface d'attaque et l'inventaire — chaque chiffre permettant d'explorer jusqu'à ses preuves. Prometheus sur `/metrics` ; webhooks sortants vers Slack, PagerDuty, ServiceNow, ConnectWise, Splunk, Datadog, Loki, Opsgenie, Sentry, NetBox, et plus.

**L'agent.** Un unique binaire Rust pour Linux, macOS et les BSD / illumos (Windows pris en charge ; les nouveaux sous-systèmes sortent d'abord pour Linux + macOS). 69 types de ressources (y compris des exécuteurs custom via JSON), 50 collecteurs de traits, gestion de VM libvirt, conteneurs de première classe et auto-mise à jour signée.

---

## Commencez ici

| | |
|---|---|
| **Vous voulez juste que ça tourne ?** | [Démarrage rapide](quickstart.md) — serveur lancé, premier Envoy enrôlé, premier configcrate appliqué, en dix minutes de copier-coller |
| **Nouveau sur Vigo ?** | [Tutoriels](start/) — le même parcours, déroulé pas à pas : monter le serveur, enrôler un Envoy, livrer votre premier configcrate, lancer votre premier rapport de conformité |
| **Besoin de faire quelque chose ?** | [Guides pratiques](howto/) — configcrates, swarm, authentification, supervision, conformité, sauvegarde/PRA, migrer depuis un autre CM |
| **Vous cherchez une information ?** | [Référence](reference/) — vigocli, ressources de l'agent, traits de l'agent, API REST, server.yaml, le langage des configcrates |
| **Vous voulez le modèle mental ?** | [Concepts](concepts/) — architecture, convergence vs conformité, le Swarm, l'identité Puddle, le modèle de sécurité |
| **Que signifie un terme ?** | [Glossaire](glossary.md) |

---
*Confidentiel — Alexander4, LLC. Ne pas redistribuer. Voir [legal/license.md](legal/license.md).*
