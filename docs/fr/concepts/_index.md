<!-- vigo-i18n source_sha:b04e3d183cca07c9464c6d09a8be6c6479fae3bf7960c2a77aaa4c27ee42cdac -->

# Concepts

Orienté compréhension. Le modèle mental derrière Vigo — comment les pièces s'assemblent et pourquoi elles ont la forme qu'elles ont. Lisez ces pages quand vous voulez le *pourquoi*, pas une étape ou un paramètre. (Pour les décisions et leur justification au niveau de l'ingénierie, voir les ADR ; ce ne sont pas des docs destinées aux opérateurs.)

- [**Architecture**](architecture.md) — serveur, agent, CLI et interface web ; la boucle check-in → résolution → application → rapport.
- [**Configcrates**](configcrates.md) — le modèle de configuration : configcrates, rôles, hostcrates, usercrates, environnements et précédence des variables.
- [**Convergence vs conformité**](convergence-vs-compliance.md) — les deux axes orthogonaux que Vigo ne croise jamais.
- [**Secrets**](secrets.md) — la convention `secret:`, la substitution par marqueur et les backends enfichables.
- [**Le Swarm**](the-swarm.md) — le substrat pair-à-pair et ses six sous-systèmes de contenu.
- [**Identité Puddle**](puddle-identity.md) — l'identité par utilisateur sur laquelle repose chaque sous-système du Swarm.
- [**Fédération Spanner**](spanner-federation.md) — partitionnement du plan de contrôle entre pairs égaux au-delà d'un seul serveur.
- [**Scrier**](scrier.md) — accès distant inversé sans aucun port entrant.
- [**Le modèle de sécurité**](security-model.md) — mTLS, signature ed25519, RBAC et la chaîne de hachage d'audit.
- [**Le modèle de conformité**](compliance-model.md) — frameworks, capacités, bundles, Connwaer et dérogations.
- [**Haute disponibilité**](high-availability.md) — réplicas pairs, Spanner et état durable.

---
*Confidentiel — Alexander4, LLC. Ne pas redistribuer.*
