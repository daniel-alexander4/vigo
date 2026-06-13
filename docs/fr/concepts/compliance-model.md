<!-- vigo-i18n source_sha:e0c4289e63169ed6ad045bd1f951750c2a98f5015a22da515c05c79eac8ca625 -->

# Le modèle de conformité

**Comment Vigo décide-t-il qu'un nœud « satisfait HIPAA » ?** Il ne devine pas et ne vous croit pas sur parole — un contrôle n'est couvert que lorsqu'un élément concret le prouve. Cette page présente le modèle mental ; la cartographie par contrôle se trouve dans la [matrice de conformité](../reference/compliance-matrix.md).

## Deux types de conformité, jamais confondus

- **Conformité de l'Envoy** — les *configcrates d'un nœud* satisfont-ils les contrôles d'un cadre réglementaire (HIPAA, SOC 2, PCI DSS, ISO 27001, …) ? Rapportée sous forme de % de couverture par cadre.
- **Conformité produit de Vigo** — ce que le *serveur et l'agent* satisfont par construction (mTLS, RBAC, la chaîne de hachage du journal d'audit, le chiffrement des secrets). Exposée sur `/platform/compliance`. Elle ne gonfle jamais le score d'un Envoy ni celui de la flotte.

Et aucune des deux n'est la [convergence](convergence-vs-compliance.md) — lisez cette page si les termes vous semblent encore glissants.

## Quand un contrôle est considéré comme couvert

Pour un Envoy et un contrôle de cadre donnés, la couverture exige l'une des conditions suivantes :

1. **Un configcrate le revendique.** Deux catalogues curatés acheminent la revendication :
   - **`provides:`** — *capacités fonctionnelles* transversales aux cadres (`time-sync`, `audit-trail`) ; le catalogue établit la correspondance de chacune avec les contrôles qu'elle satisfait.
   - **`bundle:`** — implémentations *taillées sur la portée d'un cadre*.
   - ou un fichier d'héritage `compliance.vgo` au niveau d'un répertoire. Le chargeur réunit les trois sources.
2. **Connwaer le prouve.** L'agent autonome [Connwaer](../reference/vigo/traits/connwaer.md) exécute des vérifications *actives* pour les éléments que l'application des règles ne peut démontrer (WORM, validation du RNG, chiffrement au repos, segmentation) et rapporte les constats sous forme de traits qui font passer un contrôle à l'état *satisfait*.
3. **Une dérogation s'applique** — une exception explicite et enregistrée.

## Des affirmations honnêtes

Vigo rapporte ce qu'il peut prouver et refuse ce qu'il ne peut pas. Il indique clairement ce qu'il **applique** par rapport à ce qu'il **surveille** ou ce qui **nécessite une validation externe**, et il ne revendique pas de contrôles hors de sa portée (archivage WORM à long terme, coupe-circuits inférieurs à la milliseconde). Une couverture que vous ne pouvez pas étayer n'est pas une couverture.

## Pousser les preuves vers l'extérieur

Les preuves de réussite/échec par contrôle sont poussées vers les plateformes GRC (Vanta, Drata, Secureframe, ServiceNow GRC, …) selon un calendrier, et les documents de conformité sont exportés en PDF pour les auditeurs et la cyberassurance.

## Où cela apparaît

- [Rapports de conformité](../howto/compliance/compliance-reporting.md) — générer + exporter.
- [Matrice de conformité](../reference/compliance-matrix.md) — la référence par contrôle.
- [`vigocli compliance`](../reference/vigocli/compliance.md).

---
*Confidentiel — Alexander4, LLC. Ne pas redistribuer.*
