<!-- vigo-i18n source_sha:46b5ddc8451df58d8031bac7a8ca44caa5c2496e288c186da780cda2a4a145ea -->

# Configcrates

**Comment indiquez-vous à Vigo à quoi une machine doit ressembler ?**

Vous écrivez des **configcrates** — des ensembles réutilisables de ressources — et vous les liez aux machines via un petit ensemble de primitives de composition. Un configcrate est l'unité de réutilisation ; tout le reste (roles, hostcrates, environnements) détermine *quels* configcrates atterrissent sur *quels* Envoys, avec *quelles* variables.

## Les primitives

| Primitive | Ce que c'est |
|---|---|
| **Configcrate** | Un ensemble nommé de [ressources](../reference/vigo/resources/_index.md) (`file`, `package`, `service`, …) avec des variables par défaut. Réside dans `stacks/configcrates/*.vgo`. Inerte tant que rien ne l'inclut. |
| **Role** | Un groupe nommé de configcrates (avec des groupes `case:` par OS et des `includes:` à un seul niveau). Défini une seule fois dans `roles.vgo`. |
| **Hostcrate** | Un fichier `.vgo` associant des motifs/globs de nom d'hôte → roles, configcrates et variables. **La première correspondance l'emporte.** |
| **Usercrate** | Un type de configcrate par utilisateur Unix ; inclus positivement via le champ `usercrates:` d'un porteur. |
| **Environment** | `environments.vgo` redéfinit les configcrates / variables par nom d'environnement (dev / staging / prod). |
| **`common.vgo`** | À n'importe quel niveau de répertoire, définit des configcrates/variables hérités par les sous-répertoires — le parent avant l'enfant. |

Une ressource déclare un `type` et doit être **idempotente** — vérifier avant d'agir. `depends_on` construit un DAG (les cycles sont rejetés au chargement) ; `notify` / `subscribes` réappliquent une ressource lorsqu'une dépendance change.

## Précédence des variables

Lorsqu'une même variable est définie à plusieurs endroits, la plus spécifique l'emporte :

```
environments.vgo  >  match-block vars  >  inherited vars  >  configcrate defaults
```

## Conditionnels et templates

- **`when:`** : des expressions conditionnent une ressource ou un configcrate par une logique booléenne sur des builtins de traits — `os_family('debian')`, `!is_container && has_enough_ram`. Évaluées côté serveur lorsque c'est possible, sinon transmises à l'agent.
- **Templates** : utilisent la syntaxe Go template avec `.Traits` et `.Vars`, dans les attributs `content:` ou des fichiers externes `source: templates/file.tmpl`.
- **`foreach:`** : génère une ressource — ou un configcrate entier — une fois par élément de liste.

## La seule règle qui compte

**Les configcrates ne doivent jamais nécessiter d'intervention manuelle pour récupérer.** Utilisez des gardes `onlyif` basées sur l'âge, pas des vérifications de fichiers statiques ; un configcrate qui peut se bloquer dans une boucle d'échec est défectueux. Vigo converge vers l'état désiré à chaque intervalle et *y reste* — voir [convergence vs conformité](convergence-vs-compliance.md).

## Où cela apparaît

- [Écrire votre premier configcrate](../howto/configcrates/write-your-first-configcrate.md) — pratique.
- [Écriture des configcrates](../reference/configcrate-language.md) — la référence d'écriture complète.
- [Publier la configuration](../howto/configcrates/publish-config.md) — valider + pousser vers la flotte.
