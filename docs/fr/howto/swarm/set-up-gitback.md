<!-- vigo-i18n source_sha:e7c0f215bf068d34b3748ed6956b3f7c329cb58d5a737b36259193d6ac5e0f41 -->

# Configurer Gitback

À la fin de cette page, vous aurez un projet git vivant sur chaque Envoy membre du puddle qu'un utilisateur touche — `git push` déclenche une diffusion vers des dépôts bare appartenant à root sur le `/var/lib/vigo/swarm/gitback/projects/<pid>.git` de chaque membre, et n'importe lequel de ces Envoys peut vous renvoyer un clone neuf. Le serveur Vigo reste en dehors des décisions d'appartenance ; l'identité du projet est la pubkey puddle du fondateur.

**Quand l'utiliser :** reprise après sinistre personnelle — un utilisateur, plusieurs Envoys, pas de serveur git central, aucun Envoy dont la disparition fait perdre votre dépôt. Aussi la collaboration en petite équipe où plusieurs Puddles partagent un projet.

**Quand vous en passer :** les dépôts où la confidentialité des octets sur disque importe (les dépôts bare gitback sont lisibles par root sur chaque Envoy membre — utilisez lockbox pour le secret du contenu) ; les dépôts où une forge centrale satisfait déjà le niveau de reprise après sinistre (GitHub/Gitea) ; un état mono-Envoy sans besoin de réplication.

## L'activer

Gitback repose sur puddle, donc le prérequis est l'[appartenance à un puddle](set-up-puddle.md). Activez-le aussi par utilisateur via le usercrate :

```yaml
# /srv/vigo/stacks/usercrates/dan.vgo
resources:
  - type: user
    username: dan
    puddle: true
    gitback: true             # opt-in for git-over-swarm
```

La barrière à l'échelle de la flotte se trouve dans `server.yaml` :

```yaml
swarm:
  gitback:
    enabled:
      - "*"                   # all envoys (or use first-match-wins patterns)
```

Publiez (`sudo vigocli config publish`).

## L'utiliser

```bash
# Start a project (on any envoy where you have puddle unlocked):
vigo swarm gitback project init my-config-repo

# Add the gitback remote and push:
cd my-config-repo
git remote add gitback gitback://<your-puddle-name>/my-config-repo
git push gitback main

# On another of your envoys:
git clone gitback://<your-puddle-name>/my-config-repo
```

État par puddle :

```bash
vigo swarm gitback status                          # what's local vs remote
vigo swarm gitback project list                    # all projects you have a foothold in
vigo swarm gitback project invite <other-puddle>   # for multi-member collaboration
```

L'arbre complet des verbes : `vigo swarm gitback {status,purge,gc-orphans}` plus `project {init,invite,join,leave,list,status,revoke,reanchor,re-invite-all,rejoin,re-found}`. Les opérateurs touchent rarement à `reanchor` / `re-found` — ce sont des chemins de récupération pour les scénarios de clé compromise documentés dans l'ADR-019. `gc-orphans` est de l'hygiène pour le cas post-`userdel -r` (voir [Quitter un projet](#leaving-a-project--decommissioning-a-user) ci-dessous).


## Comment ça marche

![Gitback — git push déclenche la diffusion par le daemon helper vers le dépôt bare de chaque Envoy membre](../../img/gitback.svg)

Chaque projet est indexé par `project_id = sha256(founder_puddle_pubkey ‖ handle)`, de sorte que deux opérateurs publiant `my-config` n'entrent pas en collision. La forme du projet repose sur deux axes orthogonaux :

| `members:` | `dr_scope:` | Cas d'usage |
|---|---|---|
| `{my-puddle}` | `swarm-wide` | Reprise après sinistre personnelle (défaut actuel) |
| `{my-puddle}` | `members-only` | Mono-utilisateur, pas de réplication swarm |
| `{team1, team2, …}` | `members-only` | Collaboration multi-membres uniquement |
| `{team1, team2, …}` | `swarm-wide` | Dépôt d'équipe avec reprise après sinistre à l'échelle du Swarm |

Le substrat voit des flottes, pas des Envoys. Le serveur Vigo ne joue aucun rôle dans les décisions d'appartenance — il fournit trois services légers (l'agrégateur de flotte gitbackmesh, la résolution d'adresses inter-sous-réseaux, les entrées du journal d'audit). L'appartenance est imposée par des **jetons de capacité ancrés au puddle** que le destinataire vérifie hors ligne contre le project_id de l'URL.

### Chemin push / fetch

`git push gitback ...` invoque le binaire multicall en tant que `git-remote-gitback`. L'état par utilisateur vit dans `~/.vigo-gitback/<project_id>/` ; les dépôts bare appartenant à root dans `/var/lib/vigo/swarm/gitback/projects/<project_id>.git` (mode 0700). Les appelants non-root (vos invocations `git` normales) atteignent le dépôt bare via le daemon helper sur `/var/run/vigo/gitback.sock` (protégé par SO_PEERCRED).

La diffusion du push est automatique : après une réception locale réussie, le helper fait un `POST` vers `/swarm/gitback/push/<pid>` sur chaque pair vivant qui détient un point d'appui. La reprise de reprise après sinistre initiée par le destinataire (depuis 0.46.0) gère le cas où un pair était hors ligne au moment du push — il tire lors de la connexion suivante avec un plafond de transfert de 2 Gio pour borner la charge.

### Enveloppes d'appartenance

Les opérations d'appartenance (`invite`, `leave`, `revoke`, `re-invite-all`, `rejoin`) signent des enveloppes Ed25519 via le helper de session puddle. Les enveloppes se propagent en P2P avec **reprise basée sur digest** — chaque annonce de tick de gossip porte un digest de la table d'enveloppes locale ; les discordances déclenchent un tirage de la table complète. Les pairs hors ligne pendant une révocation convergent sans intervention de l'opérateur.

### Récupération après compromission d'une clé fondatrice (ADR-019)

Le `project_id` est ancré à la pubkey fondatrice **genesis** pour toujours — c'est voulu, sinon un renommage permettrait à une clé volée d'usurper le fondateur d'origine. Si la clé puddle d'un fondateur est compromise, le chemin de récupération est la **délégation de re-clé du fondateur** : émettre une chaîne de délégation par projet (plafond 5 sauts, avertissement à 3) qui fait le pont entre la pubkey retirée et la nouvelle. Chaque vérificateur remonte la chaîne jusqu'à genesis. Au-delà du plafond, `re-found` crée un nouveau `project_id` et marque l'ancien comme tombstone.

Surface opérateur :

- `vigo swarm gitback project reanchor` — réémettre le projet vers une nouvelle pubkey puddle locale (utilisé en reprise après sinistre après `puddle rotate`).
- `vigo swarm gitback project re-invite-all` — réémettre les invitations à chaque membre existant depuis la clé de signature actuelle.
- `vigo swarm gitback project re-found` — dernier recours : nouveau project_id, ancien marqué tombstone. Les URL qui utilisaient la forme par nom convivial continuent de se résoudre via la chaîne `supersedes` de l'ADR-022 ; les URL en forme hex cassent par conception.

### URL conviviales (ADR-022)

`gitback://<project_id>/<handle>` est auto-certifiante mais peu mémorisable. La [revendication de nom](set-up-puddle.md#friendly-names-adr-022) du puddle vous permet d'écrire `gitback://<your-puddle-name>/<handle>` à la place. Le résolveur `git-remote-gitback` mappe `<puddle-name>` → pubkey puddle via la carte de noms de flotte en cache à `/var/lib/vigo/swarm/puddle/names.json`, puis calcule `sha256(pubkey ‖ handle)` et procède comme d'habitude. Il se rabat sur la forme hex en cas d'absence ou de collision ambiguë.

### Autorisation opérateur

Deux couches contrôlent gitback par Envoy. Les deux doivent dire oui :

| Couche | Où | Question |
|---|---|---|
| Envoy | liste de motifs `server.yaml#swarm.gitback.enabled` | Gitback est-il disponible sur cet Envoy tout court ? |
| Utilisateur | drapeau usercrate `gitback: bool` | Quels utilisateurs Unix sur cet Envoy peuvent l'utiliser ? |

Retirer `gitback: true` d'un usercrate déclenche un nettoyage à la prochaine réconciliation (`~/.vigo-gitback/` effacé pour cet utilisateur). Les projets déjà publiés sur cet Envoy disparaissent localement ; les autres Envoys conservent leurs copies.

**La confidentialité n'est explicitement pas un objectif.** Les octets sur disque des dépôts bare gitback sont lisibles par root sur chaque Envoy cible. Gitback est pour la *disponibilité* (votre dépôt survit à la disparition d'un Envoy quelconque) ; lockbox est le bon outil quand vous avez besoin de *confidentialité*.

### Quitter un projet / déclasser un utilisateur

`vigo swarm gitback project leave <project_id>` est le **seul** chemin qui émet une enveloppe LEAVE signée. Il écrit une sentinelle `leave.pending` que le daemon helper signe et propage ; chaque pair retire votre pubkey puddle de la liste des membres du projet. Exécuter `rm -rf ~/.vigo-gitback/<project_id>/` est local uniquement — les pairs ne l'observent pas, et au prochain `git pull` contre le projet ils resynchroniseront volontiers votre état de travail. C'est délibéré : le répertoire d'état utilisateur par projet est une copie de travail, et re-cloner est une manœuvre de récupération routinière qui ne devrait jamais signaler « je quitte le projet pour toujours ».

**Flux de travail recommandé avant `userdel -r <user>` :** en tant que cet utilisateur, quittez chaque projet explicitement :

```bash
for pid in $(vigo swarm gitback project list --json | jq -r '.[].project_id'); do
  vigo swarm gitback project leave "$pid" --yes
done
```

Ensuite `userdel -r` est sûr.

**Si `userdel -r` a déjà eu lieu sans leave préalable :** l'identité puddle de l'utilisateur parti est partie avec son répertoire personnel, donc l'agent *ne peut pas* auto-émettre un LEAVE en son nom — les enveloppes doivent être signées par le sujet. Le dépôt bare à `/var/lib/vigo/swarm/gitback/projects/<project_id>.git` est désormais orphelin localement : aucun utilisateur ne détient la copie de travail, mais les pairs continuent de lui diffuser les pushes. Exécutez `vigo swarm gitback gc-orphans` (en root) pour lister les dépôts bare sans utilisateur local détenant le projet, puis `vigo swarm gitback gc-orphans --yes` pour les supprimer. Mise en garde : si cet Envoy agissait comme réplicateur de diffusion pour un projet (dépôt bare seul, pas de copie de travail locale), le GC retire cette réplique du puddle — ne faites cela que lorsque vous êtes sûr qu'aucun autre Envoy ne dépend de celui-ci pour la disponibilité de la diffusion.

### Où regarder quand ça ne marche pas

- **`vigo swarm gitback status`** — liste des projets locaux, vivacité du daemon helper, état de la clé de signature.
- **`vigocli swarm gitback fleet`** — vue à l'échelle de la flotte : membres, refs de tête, refs divergentes, comptes d'enveloppes.
- **Interface Web `/gitback`** (admin uniquement) — appartenance par projet, visualisation de la divergence de refs à l'échelle de la flotte.
- **Pression disque** : les pushes refusent à ≤10 % d'espace libre (même règle à l'échelle de la flotte que les autres sous-systèmes Swarm).
- **ADR** : ADR-015 (substrat), ADR-019 (re-clé), ADR-022 (noms), ADR-023 (adoption à l'échelle du puddle).

## Et ensuite

- **Chiffrer le contenu qui vit dans la même flotte** → [Configurer Lockbox](set-up-lockbox.md). Gitback est pour la disponibilité ; lockbox est pour la confidentialité.
- **Pousser des artefacts curatés par l'admin (binaires, fichiers de modèles, fixtures) sur le même substrat** → [Configurer Curator](set-up-curator.md).
- **Une clé puddle a été compromise** → réémettre les invitations avec `vigo swarm gitback project re-invite-all` ou, au-delà du plafond de délégation, `re-found`. Consultez l'ADR-019 avant d'agir.
- **`git push` se bloque ou refuse** → [Résoudre les problèmes courants](../operate/troubleshoot.md).

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*
