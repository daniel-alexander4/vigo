<!-- vigo-i18n source_sha:0d662af52cbaa20e3fdf64596001ab03465e37f9595a671fe840ab76ee847b13 -->

# 4 · Votre premier rapport de conformité

À la fin de cette page, vous aurez consulté la couverture de votre flotte par rapport à un cadre réglementaire et exporté un rapport. Cinq minutes — et cela boucle la boucle entre l'*application* et la *preuve*.

## Prérequis

- [Tutoriel 3](your-first-configcrate.md) terminé — au moins un envoy qui converge avec un configcrate lié.

## 1. Voir où vous en êtes

Ouvrez la page **Compliance** dans l'interface web. Choisissez un cadre (HIPAA, SOC 2, PCI DSS, …). Vous verrez un **pourcentage de couverture** et une ventilation par contrôle : quels contrôles sont couverts, et *comment* chacun l'est — par la déclaration d'un configcrate, une vérification [Connwaer](../concepts/compliance-model.md), ou une dérogation.

![La page Compliance — score de couverture de la flotte, décomptes satisfait/partiel/non atteint, et exploration par cadre](../img/screenshots/vigo.compliance.png)

Votre chiffre sera faible pour le moment — vous n'avez écrit qu'un seul configcrate. C'est justement le but : la couverture reflète ce que vous pouvez réellement **prouver**, pas ce que vous espérez vrai. Voir [convergence vs conformité](../concepts/convergence-vs-compliance.md) pour comprendre pourquoi ce sont des questions différentes.

## 2. Faire passer un contrôle au vert

Choisissez un contrôle non couvert qu'un configcrate pourrait satisfaire, et déclarez-le. La méthode la plus propre est un tag `provides:` sur un configcrate — le catalogue de Vigo fait correspondre un tag de capacité aux contrôles qu'il satisfait dans tous les cadres :

```yaml
configcrate: ntp
compliance:
  provides: [time-sync]
resources:
  - type: service
    name: chrony
    state: running
    enabled: true
```

`vigocli config publish`, attendez un intervalle, et rafraîchissez la page Compliance — les contrôles liés à `time-sync` basculent en couverts, et votre pourcentage augmente. Vous venez de transformer l'application en preuve de conformité.

## 3. L'exporter

Générez un rapport pour un auditeur :

```bash
vigocli compliance report --framework hipaa        # human-readable / PDF
vigocli compliance export --format oscal           # machine-readable for GRC tools
```

Les rapports indiquent clairement ce que Vigo **applique** vs **surveille** vs **exige une validation externe** — des déclarations honnêtes, pas gonflées.

## C'est terminé

Vous avez mis en place un serveur, enrôlé des envoys, écrit et fait converger un configcrate, observé la correction d'une dérive, et transformé cette application en couverture de cadre que vous pouvez remettre à un auditeur. C'est l'arc complet.

## Où aller ensuite

- **[Concepts](../concepts/_index.md)** — le modèle mental derrière ce que vous venez de faire.
- **[Guides pratiques](../howto/_index.md)** — les sous-systèmes Swarm, l'authentification, la surveillance, la sauvegarde/reprise, la migration depuis un autre CM.
- **[Référence](../reference/_index.md)** — chaque commande, ressource, trait et clé de configuration.

---
*Confidentiel — Alexander4, LLC. Reproduction interdite.*
