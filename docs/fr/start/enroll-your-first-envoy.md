<!-- vigo-i18n source_sha:9980a02575243600e3e288944635336f2baa57284e28a1cb7fd09d81c2260bc7 -->

# 2 · Enrôlez votre premier Envoy

À la fin de cette page, une *seconde* machine — un véritable nœud géré — fera son check-in auprès de votre serveur. Cinq minutes.

La mise en route du serveur a enrôlé son propre hôte via la loopback ([tutoriel 1](stand-up-the-server.md)). Enrôler une machine *distante* est un chemin distinct : cela nécessite un jeton à usage unique, car elle ne se trouve pas sur le CIDR loopback auto-signé.

## Prérequis

- [Tutoriel 1](stand-up-the-server.md) terminé — un serveur en fonctionnement.
- Un second hôte Linux/macOS capable de joindre le serveur sur `:1530` et `:8443`.
- `vigocli` configuré pour dialoguer avec votre serveur (sur l'hôte du serveur, c'est déjà le cas).

## 1. Émettre un jeton à usage unique

Décidez quels noms d'hôte vous autorisez à s'enrôler, puis émettez un jeton qui leur est restreint :

```bash
vigocli tokens generate --pattern '*.example.com'
```

Cela affiche un jeton à usage unique (haché au repos côté serveur ; il ne peut pas être réutilisé). Le `--pattern` est un garde-fou d'enrôlement — seuls les noms d'hôte qui le respectent peuvent s'enregistrer.

## 2. Lancer l'installateur sur la nouvelle machine

Sur le **nouvel** hôte, récupérez et lancez l'installateur bootstrap en passant le jeton :

```bash
curl -sSfk https://<server-host>:8443/bootstrap | sudo sh -s -- --token <token>
```

L'installateur télécharge le binaire de l'agent, génère un CSR, s'enrôle (le serveur le signe après avoir vérifié le pattern + le jeton), installe un service natif (systemd / launchd / …) et le démarre. L'agent est installé dans `/usr/local/sbin/vigo`.

## 3. Le voir apparaître

De retour dans le tableau de bord de l'interface web, **en l'espace d'un intervalle de check-in, le nouvel Envoy apparaît** avec son nom d'hôte, ses traits et un statut vert. Ou depuis la CLI :

```bash
vigocli envoys list
```

Vous verrez les deux Envoys — l'hôte propre du serveur et celui que vous venez d'enrôler.

## Point de contrôle

Deux Envoys, tous deux en check-in. Rien n'est encore *appliqué* — vous n'avez écrit aucune config. C'est l'étape suivante.

**Suivant :** [Votre premier configcrate →](your-first-configcrate.md)

Voir aussi : [Enrôler un Envoy](../howto/operate/enroll-an-envoy.md) pour la procédure complète (installations hors ligne, gestionnaires de services personnalisés, ré-enrôlement).

---
*Confidentiel — Alexander4, LLC. Ne pas redistribuer.*
