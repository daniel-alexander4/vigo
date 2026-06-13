<!-- vigo-i18n source_sha:bdbbded2295d63b00174ed95a3b9ff0b72c9b8e5bbcc0f0dc6d313e2b25a6791 -->

# Le modèle de sécurité

**Qu'est-ce qui arrête un acteur malveillant — sur le réseau, au niveau du serveur et sur le nœud ?** La réponse de Vigo est un ensemble de contraintes qui tiennent par construction, et non par une configuration que vous pourriez oublier.

## Les principes non négociables

Ce sont des invariants, pas des options :

- **Tout le gRPC est en mTLS.** Aucun transport en clair, jamais.
- **Chaque requête d'agent est signée en ed25519** par rapport à la clé publique stockée à l'inscription. Une requête falsifiée ou rejouée provenant d'une clé inconnue est rejetée.
- **Les jetons d'inscription à usage unique sont hachés au repos**, et un client révoqué reçoit une erreur au check-in et ne peut pas se réinscrire avec le même UUID.
- **Les secrets n'apparaissent jamais en clair** dans la configuration, les variables d'environnement, les journaux, la base de données, les charges utiles gRPC ou les résultats d'exécution — voir [secrets](secrets.md).
- **Tous les exécuteurs de configcrate sont idempotents** — vérifier avant d'agir — de sorte qu'une ressource réappliquée ne peut pas causer de dommage deux fois.

## Authentification et autorisation

- **Authentification enfichable** (`basic` / `oidc` / `isowebauth`), toujours requise. Sessions dans SQLite ; les jetons d'API portent un préfixe `vgot_` et sont hachés en SHA-256 au repos (un jeton aléatoire de 256 bits n'a pas besoin de bcrypt, et un bcrypt par requête constituerait une surface de DoS).
- **RBAC** : `admin` / `viewer` / `compliance`. Le rôle compliance se voit refuser `audit.read` — le journal d'audit brut en chaîne de hachage est réservé aux rôles admin/viewer.
- Sur l'hôte serveur, `vigocli` s'authentifie avec un jeton d'administration local frappé au démarrage, réservé à root ; l'adresse IP source n'accorde rien.

## Inviolabilité et durcissement

- **Le journal d'audit est une chaîne de hachage SHA-256** sur chaque opération privilégiée — vous ne pouvez pas modifier discrètement l'historique sans rompre la chaîne. Il est également diffusé vers les SIEM via le répartiteur d'intégrations.
- **Le durcissement au démarrage échoue en mode fermé** : une vérification de signature binaire ed25519 détachée conditionne le démarrage *avant* que le serveur ne soit construit. La détection de débogueur / d'injection est en mode avertissement uniquement (heuristiques trop grossières pour échouer en mode fermé) et est enregistrée comme événement d'audit.
- **L'agent ne se met à jour qu'à partir de binaires signés en ed25519** (SHA-256 + signature vérifiés, remplacement atomique), et des garde-fous protègent les chemins/services/paquets et analysent les commandes dangereuses.

## Sa forme générale

La confiance est **ancrée dans les clés, pas dans les réseaux** : mTLS pour le canal, ed25519 pour chaque requête et chaque binaire, hachage pour les jetons et la chaîne d'audit. Une position réseau compromise n'apporte à un attaquant rien qu'il ne puisse prouver cryptographiquement.

## Où cela se manifeste

- [Secrets](secrets.md) · [Scrier](scrier.md) · [Identité Puddle](puddle-identity.md).
- L'interface `/security` (CVE, surface d'attaque, binaires signés, avertissements de durcissement).

---
*Confidentiel — Alexander4, LLC. Ne pas redistribuer.*
