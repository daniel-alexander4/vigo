<!-- vigo-i18n source_sha:bde3e89a647e523babf914ee52ab6d7f72c046f59ee477bc7330ac2d5a5b510d -->

# Convergence et conformité

**Deux mots qui se ressemblent et désignent des choses totalement différentes dans Vigo.** Vigo ne les confond jamais, et vous ne devriez pas non plus.

## La réponse courte

- La **convergence** répond à : *cet Envoy est-il dans l'état que mes configcrates déclarent ?* Il s'agit d'application — les ressources se sont-elles appliquées, et y a-t-il une dérive ?
- La **conformité** répond à : *cet Envoy satisfait-il un cadre réglementaire ?* Il s'agit de couverture — quelle proportion de HIPAA / SOC 2 / PCI est démontrablement satisfaite ?

Un Envoy peut être parfaitement **convergé** (chaque configcrate appliqué proprement) et néanmoins **non conforme** (personne n'a écrit de configcrate couvrant le contrôle de chiffrement au repos). L'inverse se produit aussi.

## Convergence — deux axes orthogonaux plus la joignabilité

Gérée par `server/convergence/` et `server/freshness/` :

- **Échec** (`Status`) : **Converged** / **Degraded** / **Failed** / **NoData** — mutuellement exclusifs, leur somme couvre le parc.
- **Dérive** (`DriftLevel`) : **None** / **Changed** / **Diverged** — indépendante de l'échec ; N exécutions consécutives comportant des changements font basculer un Envoy en *Diverged*.
- **Joignabilité** — l'Envoy fait-il seulement son check-in ? Un prédicat d'obsolescence par Envoy (`2.5 ×` sa cadence observée), distinct des deux axes.

Ces axes sont indépendants : un Envoy convergé peut dériver ; un Envoy joignable peut être en échec.

## Conformité — couverture par rapport aux cadres

Un contrôle est considéré comme **couvert** pour un Envoy lorsque l'une de ces conditions est remplie :

1. un configcrate qu'il possède *revendique* le contrôle — via `provides:` (capacités fonctionnelles) ou `bundle:` (coupes de portée par cadre), ou un fichier `compliance.vgo` au niveau du répertoire ;
2. un contrôle actif [Connwaer](compliance-model.md) le fait passer à *satisfait* ; ou
3. une dérogation s'applique.

La couverture est un pourcentage par cadre. Voir [le modèle de conformité](compliance-model.md).

## Pourquoi les garder séparés

Les confondre produit un reporting malhonnête — « 100 % convergé » n'est pas « 100 % conforme », et vendre l'un pour l'autre est précisément ainsi que naît le théâtre de la conformité. Vigo rapporte ce qu'il **applique** (convergence) séparément de ce qu'il **démontre par rapport à une norme** (conformité), et ne gonfle jamais l'un avec l'autre.

## Où cela apparaît

- Les cartes de convergence et de conformité du tableau de bord (séparées, avec un détail propre à chacune).
- [Reporting de conformité](../howto/compliance/compliance-reporting.md) — générer la couverture par cadre.
- [Configcrates](configcrates.md) — ce que la convergence applique.

---
*Confidentiel — Alexander4, LLC. Ne pas redistribuer.*
