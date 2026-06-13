<!-- vigo-i18n source_sha:dc73f096b4e76075a7c947bbe0cdfc5e91a893c8d7c0ab8274233c8505df80d4 -->

# Durcir un Envoy contre la divulgation des secrets en mémoire

Lorsqu'un Envoy applique un configcrate qui référence un `secret:`, l'agent récupère
le texte en clair depuis le serveur et le conserve **en mémoire de processus** pendant
toute la durée de cette époque de secret — dans un cache de secrets résolus, ainsi que
dans les copies éphémères que crée le pipeline substitution → rendu → application (le
`content:` du fichier rendu, les valeurs d'environnement d'exec, les chaînes de commande).
C'est inévitable : l'agent doit disposer du texte en clair pour écrire le fichier ou exécuter
la commande. La question à laquelle répond cette page est : comment empêcher ce texte en clair
d'atteindre le **disque** ou **un autre processus**.

La majeure partie de cette frontière est déjà appliquée par l'agent lui-même. Le seul élément
qui relève de l'opérateur est le **swap**.

## Ce que l'agent fait déjà

Au démarrage, avant de résoudre le moindre secret, l'agent durcit son propre processus
(`harden_process_memory`, au mieux, journalisé en cas d'échec) :

| Mesure | Plateforme | Ce qu'elle empêche |
|---|---|---|
| `RLIMIT_CORE = 0` | tous les Unix | Un plantage ne produit aucun core dump, de sorte que les octets du secret ne peuvent pas être capturés dans un fichier core sur le disque. |
| `PR_SET_DUMPABLE = 0` | Linux | Marque le processus comme non dumpable — bloque l'attachement `ptrace` et les lectures de `/proc/<pid>/mem` par un autre processus du même uid. |

Vous ne configurez pas ces mesures ; elles sont toujours actives. Pour confirmer que l'agent
s'exécute en mode non dumpable sous Linux :

```sh
# 0 = non-dumpable (hardened). Run as the user the agent runs as (root).
cat /proc/$(pgrep -x vigo)/status | grep -i dumpable
# Dumpable:  0
```

## Le résidu : le swap

Ce que le processus **ne peut pas** fermer proprement de lui-même, c'est le swap. Sous
pression mémoire, le noyau peut paginer n'importe quelle partie de la mémoire de l'agent —
y compris les pages contenant les secrets résolus — vers le périphérique de swap, où le
texte en clair se retrouve alors sur le disque, hors du contrôle de l'agent. L'agent
n'appelle délibérément **pas** `mlockall` pour épingler sa mémoire en RAM : cela irait à
l'encontre de la doctrine d'empreinte bornée et d'invisibilité autour de laquelle l'agent
est conçu (et cela peut échouer purement et simplement sous un `RLIMIT_MEMLOCK` restrictif).
Fermer le chemin du swap est donc une décision de configuration de l'hôte, et elle revient
à l'opérateur.

Sur tout hôte où des Envoys résolvent des secrets, faites l'une des choses suivantes.

### Option A — pas de swap (le plus simple)

Si l'hôte est dimensionné de façon à ne pas nécessiter de swap, désactivez-le. Les pages
de secrets ne peuvent jamais atteindre un périphérique qui n'existe pas.

```sh
sudo swapoff -a
# Then remove or comment the swap entries in /etc/fstab so it stays off
# across reboots, e.g.:
#   sudo sed -i.bak '/\sswap\s/s/^/#/' /etc/fstab
```

### Option B — swap chiffré (conserver le swap, fermer l'exposition)

Si l'hôte a besoin de swap, chiffrez-le pour que tout ce qui est paginé soit illisible au
repos. Deux approches courantes :

- **`zram`** — un périphérique de swap compressé en RAM (ne touche jamais le disque).
  Adapté aux hôtes qui ont des pics occasionnels mais disposent d'une marge de RAM. La
  plupart des distributions fournissent un `zram-generator` / `systemd-zram-setup@.service`.
- **swap dm-crypt avec une clé éphémère** — une cible de swap `cryptsetup` dont la clé
  provient de `/dev/urandom` au démarrage, de sorte que le contenu du swap est irrécupérable
  après une coupure de courant. Sous Debian/Ubuntu, il s'agit d'une entrée `/etc/crypttab`
  telle que :

  ```
  # /etc/crypttab
  cryptswap  /dev/disk/by-uuid/<SWAP-UUID>  /dev/urandom  swap,cipher=aes-xts-plain64,size=256
  ```

  avec la ligne `/dev/mapper/cryptswap` correspondante dans `/etc/fstab`.

L'une ou l'autre option ferme le résidu. Choisissez selon que l'hôte a besoin d'une réelle
capacité de swap (dm-crypt) ou simplement d'une marge de pointe (zram / aucun).

## Portée

Cette page concerne le durcissement de l'**hôte sur lequel l'agent s'exécute** contre la
divulgation de la mémoire de secrets de l'agent lui-même. Elle se distingue de
[`examples/security-hardening.md`](../../examples/security-hardening.md), qui porte sur
l'utilisation de *configcrates* Vigo pour durcir les nœuds gérés eux-mêmes (pare-feu,
SSH, politique sudo).

---
*Confidentiel — Alexander4, LLC. Ne pas redistribuer. Voir [../legal/license.md](../../legal/license.md).*
