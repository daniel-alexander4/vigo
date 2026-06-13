<!-- vigo-i18n source_sha:73d9428a764ce2f2dada279cfbaba8e9c20f6f3c63760e387b07ef066f162a8e -->

# Scrier

**Comment obtenir un shell interactif sur un nœud dont aucun port entrant n'est ouvert ?** Vous ne vous connectez pas *à* lui — c'est lui qui se connecte *vers l'extérieur*, et **Scrier** emprunte cette connexion déjà établie.

## La réponse courte

Scrier donne à un administrateur une session SSH, RDP ou VNC en direct vers n'importe quel Envoy **sans ouvrir un seul port entrant sur le nœud**. La session ressort via l'`AgentStream` déjà ouvert de l'agent, le flux d'octets étant transporté sur un `TunnelStream` distinct. Du point de vue du nœud, rien de nouveau n'écoute ; c'est la même connexion mTLS sortante qu'il maintient en permanence.

## Pourquoi c'est sûr par construction

- **Aucune surface entrante.** L'agent n'accepte jamais de connexion pour Scrier ; il appelle `127.0.0.1:{port}` *localement* et relaie les octets sur son propre flux. Il n'existe aucun chemin de déplacement latéral — le tunnel ne peut atteindre que la boucle locale sur la cible.
- **Clés transitoires, propres à chaque session.** Chaque session installe une nouvelle paire de clés Ed25519 propre à la session dans le `authorized_keys` de l'utilisateur correspondant, utilisée une seule fois puis supprimée.
- **root n'est jamais une cible d'atterrissage.** Scrier fait atterrir l'opérateur dans la session d'un utilisateur *ordinaire*.
- **Le bon utilisateur, prouvé par clé.** La `ssh_public_key` d'un utilisateur web est la source de vérification : l'agent parcourt chaque utilisateur OS ordinaire (via la libc `getpwent`) et fait atterrir l'opérateur dans l'utilisateur dont le `authorized_keys` contient cette clé publique.

RDP et VNC empruntent un sidecar `guacd` afin que la session graphique s'affiche dans le navigateur.

## Le modèle mental

Voyez cela comme un **accès distant inversé** : au lieu de « opérateur → pare-feu → nœud », c'est « nœud → serveur ← opérateur », le serveur servant d'intermédiaire pour un tunnel sur des connexions qui existent déjà. C'est ce qui le fait fonctionner à travers le NAT, derrière les pare-feu, sur des réseaux que vous ne contrôlez pas.

## Où cela apparaît

- [Configurer Scrier](../howto/operate/set-up-scrier.md) — correspondances d'utilisateurs, clés SSH, première session.
- [`vigocli scrier`](../reference/vigocli/scrier.md) + le terminal navigateur dans l'UI.
- [Le modèle de sécurité](security-model.md) — où se situe Scrier dans la posture.

---
*Confidentiel — Alexander4, LLC. Reproduction interdite.*
