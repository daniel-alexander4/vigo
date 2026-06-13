<!-- vigo-i18n source_sha:feeaad7b2cdc633380273885ccdce6351a6d58fd7de90740a92da669b2d1960d -->

# 3 · Votre premier configcrate

À la fin de cette page, vous aurez écrit un petit [configcrate](../concepts/configcrates.md), publié, et observé un envoy converger vers son état sur le tableau de bord. Dix minutes — c'est la boucle fondamentale de Vigo.

## Prérequis

- [Tutoriel 2](enroll-your-first-envoy.md) terminé — au moins un envoy qui s'enregistre.
- Accès shell aux stacks du serveur (`/srv/vigo/stacks/`).

## 1. Écrire un configcrate

Un configcrate est un ensemble nommé de ressources idempotentes. Créez `configcrates/motd.vgo` sous les stacks :

```yaml
configcrate: motd
resources:
  - type: file
    target_path: /etc/motd
    content: |
      Managed by Vigo. Do not edit by hand.
    mode: "0644"
```

Voilà un configcrate complet — une ressource `file`, idempotente (Vigo vérifie le hash du contenu avant d'écrire).

## 2. Le lier à un envoy

Un configcrate est inerte tant que rien ne l'inclut. Ajoutez un bloc match (par convention dans `envoys.vgo`) qui associe un motif de nom d'hôte à ce configcrate :

```yaml
envoys:
  - match: "*"
    configcrates: [motd]
```

(`*` cible chaque envoy — acceptable pour un premier essai ; les vraies flottes ciblent par motif, **le premier match l'emporte**.)

## 3. Publier

Validez et mettez-le en production :

```bash
vigocli config publish
```

Publish vérifie chaque `.vgo` par lint, synchronise vers `.live/` et déclenche un rechargement du serveur. Une exécution propre rapporte les fichiers qu'elle a publiés ; un fichier invalide est rejeté ici, avant d'atteindre le moindre envoy.

## 4. L'observer converger

C'est la récompense. En l'espace d'un intervalle d'enregistrement, l'agent récupère le nouveau bundle, applique la ressource `file` et rend compte. Sur le tableau de bord, le **statut de convergence de l'envoy affiche l'exécution** — une ressource modifiée, puis stable. Confirmez sur le nœud :

```bash
cat /etc/motd      # → "Managed by Vigo. Do not edit by hand."
```

Maintenant, éditez `/etc/motd` à la main sur le nœud et attendez un intervalle : Vigo détecte la dérive et **la remet en état**. C'est l'enforcement — il converge et *y reste*.

## Point de contrôle

Vous avez écrit, lié, publié et fait converger un configcrate, et vu une dérive corrigée. C'est tout le modèle ; le reste n'est que davantage de ressources, un ciblage plus fin et de la composition.

**Suite :** [Votre premier rapport de conformité →](your-first-compliance-report.md)

Voir aussi : [Écrire votre premier configcrate](../howto/configcrates/write-your-first-configcrate.md) et la [référence de rédaction des configcrates](../reference/configcrate-language.md) pour la surface complète (conditionnels, templates, rôles, environnements).

---
*Confidentiel — Alexander4, LLC. Ne pas redistribuer.*
