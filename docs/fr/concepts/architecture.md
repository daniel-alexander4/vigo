<!-- vigo-i18n source_sha:aa50e42f5d9cb230460256ef1f052aaccd19caf34a882e6d825314867e6cb11f -->

# Architecture

**Comment les pièces sont-elles agencées, et qu'est-ce qui parle à quoi ?**

Vigo comporte trois binaires et une boucle. Un **serveur** central (`vigosrv`) détient l'état désiré ; un **agent** Rust (`vigo`) sur chaque nœud géré (un *envoy*) récupère cet état, l'applique et rend compte ; une **CLI** d'administration (`vigocli`) et une **interface web** HTMX pilotent l'API REST du serveur. C'est toujours l'agent qui initie — le serveur ne se connecte jamais à un nœud — de sorte que les Envoys derrière un NAT ou un pare-feu n'ont besoin d'aucun port entrant.

![Architecture Vigo — la boucle de check-in](../img/diagrams/architecture.svg)

## La boucle de check-in

Tout ce que fait un Envoy se produit à intervalle fixe, en trois étapes :

1. **CheckIn** — l'agent appelle le serveur via gRPC/mTLS avec son UUID, une empreinte de son état actuel et ses [traits](../reference/vigo/traits/_index.md) (faits auto-découverts : OS, matériel, paquets, …). Chaque requête est signée en ed25519 contre la clé publique que le serveur a stockée lors de l'enrôlement.
2. **Resolve → PolicyBundle** — le serveur fait correspondre le nom d'hôte de l'Envoy aux [hostcrates](configcrates.md) (la première correspondance l'emporte), résout les rôles → [configcrates](configcrates.md) → ressources, fusionne les variables et les surcharges par environnement, construit le DAG de dépendances et renvoie un bundle ordonné. Les références `secret:` sont remplacées par des marqueurs opaques — le texte en clair ne traverse jamais le réseau.
3. **Apply → ReportResult** — l'agent applique chaque ressource de manière idempotente (vérifier avant d'agir), respecte l'ordonnancement `depends_on` et les déclencheurs `notify`/`subscribes`, récupère les valeurs réelles des secrets au moment de l'application, et renvoie les résultats par ressource au serveur, qui les stocke.

Le modèle est **pull** : il n'y a pas de canal push du serveur vers le nœud pour le travail de routine. Lorsqu'un opérateur a besoin d'immédiateté (un force-push, une tâche ad-hoc, une session [Scrier](scrier.md)), le serveur utilise le flux déjà ouvert de l'agent plutôt que de se connecter.

## Pourquoi cette forme

- **L'agent survit à l'absence du serveur.** Il met en cache le dernier bundle, ses traits et une file de résultats dans un magasin LMDB embarqué, de sorte qu'il continue de converger hors ligne et vide ses résultats à la reconnexion. Voir [convergence vs conformité](convergence-vs-compliance.md).
- **Les fichiers sont la seule source de vérité.** Les opérateurs éditent du YAML (`.vgo`) sous les stacks ; `vigocli config publish` valide et recharge. Il n'existe aucun chemin API/UI/DB qui écrive la configuration.
- **Un seul serveur passe à l'échelle de dizaines de milliers d'Envoys à une cadence serrée**, et au-delà, [spanner](spanner-federation.md) fédère des pairs égaux en écriture.

## Où cela se retrouve

- [Configcrates](configcrates.md) — comment l'état désiré est écrit et résolu.
- [Le modèle de sécurité](security-model.md) — mTLS, signature, RBAC, la chaîne d'audit.
- [Installer le serveur](../howto/operate/install.md) · [Enrôler un envoy](../howto/operate/enroll-an-envoy.md) — la boucle, mise en place pour de vrai.

---
*Confidentiel — Alexander4, LLC. Ne pas redistribuer.*
