<!-- vigo-i18n source_sha:6922210994141b000a3cfa42aff919a24b94d390a014a285bb27402e90b0bee0 -->

# Dimensionnement d'un hôte vigosrv

À la fin de cette page, vous saurez **combien d'envoys un serveur peut héberger confortablement**, ce qui se passe au-delà de cette limite, et les leviers opérateur (marge verticale, fédération horizontale) qui la déplacent.

**Quand l'utiliser :** avant de mettre en place une nouvelle flotte, lors de la planification d'un renouvellement matériel, ou lorsque l'utilisation mémoire d'un serveur existant grimpe sous les vagues de convergence.

**Quand l'ignorer :** petites flottes — tout ce qui est en dessous de quelques milliers d'envoys tourne bien en deçà du régime traité par cette page, avec la configuration par défaut et n'importe quelle VM modeste.

> Tous les chiffres dérivent du registre de mesures [`numbers-of-record.md`](../../internal.docs/numbers-of-record.md) (la source unique de vérité). La ligne **8 vCPU / 32 GB** est l'ancrage mesuré ; le reste s'en déduit par mise à l'échelle.

## En résumé

**Deux plafonds — la mémoire, et à cadence rapide, le CPU — et lequel s'applique dépend entièrement de votre cadence de check-in.**

- Aux **cadences normales (30 s–5 m)**, le serveur est **limité par la mémoire**. Prévoyez **~1 GB de RAM serveur pour 1 000 envoys** et vous êtes correctement dimensionné. C'est la limite contraignante pour la plupart des flottes.
- À **cadence agressive (≤ 5 s)**, le **CPU** par check-in pour vérifier et traiter chaque contact devient contraignant en premier. Prévoyez **~1 vCPU pour 1 000 envoys à une cadence de 1 s** (×N pour une cadence de N secondes — à 5 s c'est ~1 vCPU pour 5 000).
- Le **point de bascule est ≈ 4–6 s** : plus rapide → dimensionnez pour le CPU ; plus lent → dimensionnez pour la mémoire.

## Le tableau de dimensionnement

Nombre maximal d'envoys par machine. Construit de manière **conservatrice** — la colonne CPU utilise le coût le plus lourd du sondage unaire (~0,80 ms/check-in) et la colonne RAM l'empreinte la plus lourde du flux maintenu (~623 KB/envoy), de sorte que vous êtes en sécurité quel que soit le chemin emprunté par vos agents. La marge est intégrée : CPU dimensionné à 75 % des cœurs, RAM à ~60 % de la mémoire physique.

| Serveur (vCPU / RAM) | Agressif **≤ 5 s** (limité CPU, @1 s) | Normal **30 s – 5 m** (limité RAM) | Base |
|---|---|---|---|
| 4 vCPU / 16 GB | ~3 700 | **~15 000** ⚠️ restez bien en dessous | mesuré (brownout) |
| **8 vCPU / 32 GB** | **~7 500** | **~30 000** | **ancrage mesuré** |
| 16 vCPU / 64 GB | ~15 000 | ~60 000 | calculé |
| 32 vCPU / 128 GB | ~30 000 | ~123 000 | calculé |
| 32 vCPU / 256 GB | ~30 000 | ~245 000 | calculé (RAM non vérifiée) |

**Comment l'utiliser :**

1. Choisissez votre cadence. La plupart des flottes tournent à 30 s–5 m → lisez la colonne **limité RAM**.
2. Trouvez votre nombre d'envoys → choisissez la plus petite machine dont le nombre le dépasse.
3. Vous tournez à ≤ 5 s ? Vérifiez aussi la colonne CPU et prenez le **plus petit** des deux.

**Exemple :** 25 000 envoys à 60 s → limité par la mémoire → **8 vCPU / 32 GB** (marge de 30k). La *même* flotte à une cadence de 1 s est limitée par le CPU à ~7 500 sur cette machine, donc vous passeriez à **32 vCPU / 128 GB**.

## Règles empiriques (la méthode rapide)

- **RAM : ~1 GB de RAM serveur pour 1 000 envoys** (~623 KB/envoy de tas vivant + base négligeable, maintenu sous ~60 % de la RAM physique). C'est généralement la limite contraignante.
- **Le CPU ne compte qu'à cadence rapide :** ~1 vCPU pour 1 000 envoys à 1 s ; ~1 vCPU pour 5 000 à 5 s ; à 30 s+ le CPU n'est pas un problème (il évolue avec la cadence).
- **Point de bascule ≈ 4–6 s.** À 30 s / 5 m vous êtes toujours limité par la RAM — dimensionnez par la RAM.

## Mises en garde — lisez-les avant de faire confiance à une ligne

- **⚠️ Les petites machines (≤ 16 GB) se dégradent, elles ne plantent pas.** À mesure qu'une machine 4/16 approche de sa limite RAM, elle entre dans un **brownout GC** — sous la valeur par défaut auto-`GOMEMLIMIT`, elle dépense presque tout le CPU en ramasse-miettes pour honorer la limite souple, l'UI web / l'API / les métriques deviennent insensibles pendant ~1,5 min, et la machine n'atteint jamais un OOM-kill propre. Le plafond utile sur les petites machines est l'*apparition du brownout*, bien en dessous d'un chiffre linéaire naïf — **restez bien en dessous du nombre et ne faites pas tourner les petites machines près de la limite.** (Les machines plus grandes avec plus de cœurs atteignent un délestage/OOM propre à la place ; le couplage GC/CPU qui provoque le brownout s'atténue au-delà de ~6–8 cœurs.)
- **Mesuré à ~150 KB d'inventaire par envoy.** Les flottes riches en paquets (~280 KB — nombreux paquets installés, CVE, certificats) tournent ~20 % plus lourd en RAM → retranchez ~15–20 % des comptes limités par la RAM.
- **Les lignes 128–256 GB sont calculées, pas mesurées** — la vérification RAM à grande échelle (250k) est en attente. La ligne 8/32 est l'ancrage mesuré à partir duquel tout est mis à l'échelle.
- **Les flottes à flux maintenu** (la valeur par défaut — chaque envoy en ligne maintient un AgentStream persistant) sont en réalité un peu *plus légères* que ce tableau conservateur sur le CPU (~0,56 ms contre 0,80 ms/check-in) et le tableau utilise déjà leur chiffre RAM le plus lourd — vous avez donc de la marge, pas un déficit.

## Pourquoi le plafond est la mémoire par envoy

Chaque envoy connecté coûte **~623 KB de tas serveur vivant** : l'entrée FleetIndex en mémoire (~275 KB — l'arbre de traits aplati et indexé), l'inventaire complet mis en cache qu'il a signalé en dernier (~150 KB pour une charge utile réaliste de 150 KB), et la connexion AgentStream maintenue (~200 KB — tampons de lecture/écriture gRPC, trois goroutines et leurs piles, état du flux HTTP/2 et du contrôle de flux, marge GC). Un sondeur unaire non maintenu coûte ~554 KB — il économise ~69 KB en ne maintenant pas le flux bidirectionnel. Le coût est linéaire et prévisible, c'est pourquoi le dimensionnement est une simple multiplication : doublez la RAM de l'hôte, doublez le plafond d'envoys.

> **Les recommandations de dimensionnement antérieures (≤ 0.69.x) citaient ~220 KB/conn et ~60k sur un hôte de 31 GiB.** Il s'agissait d'une flotte *toy-blob* sans cache d'inventaire par envoy — supplantée une fois que les split-traits de l'ADR-033 ont placé un inventaire réaliste de ~150 KB dans le FleetIndex. Le chiffre réel, conscient de l'inventaire, est ~623 KB/envoy (≈ 30k confortable / ~45k mur dur sur 8 vCPU / 32 GB), soit environ **la moitié** de l'ancien nombre. Dimensionnez à partir de cette page, pas de l'ancienne.

## Réglages qui déplacent la limite

Ceux-ci vivent dans `server.yaml` (voir [server-yaml.md](../../reference/server-yaml.md)) et échangent une ressource contre une autre. Aucun ne transforme à lui seul un petit hôte en un grand hôte confortable — ils ajustent un hôte proche de sa limite.

- **`checkin.max_connections`** (par défaut `0` → auto-dérivé de la RAM de l'hôte ; `-1` → sans plafond). Plafonne les flux d'agent maintenus simultanément, refusant l'excédent avec `ResourceExhausted` (l'agent se replie et réessaie) afin que le serveur déleste au lieu de grimper vers le mur. **Note :** le plafond auto-dérivé est construit à partir d'une estimation du tampon de connexion et est *optimiste* face au coût conscient de l'inventaire de ~623 KB/envoy — sur une machine limitée par la mémoire, il peut se situer *au-dessus* du plafond sûr réel, alors ne considérez pas le plafond seul comme votre limite ; dimensionnez selon le tableau ci-dessus et fixez un nombre explicite si nécessaire. Journalisé au démarrage comme `held-stream admission cap auto-derived from host memory`.
- **`GOMEMLIMIT`** (limite souple de mémoire de Go). **Auto-dérivé à ~80 % du budget mémoire conscient du cgroup** — la marge de ~20 % est une marge de sécurité transitoire contre le dépassement GC. Il échange du CPU contre la survie (et sur les petites machines cet échange devient le brownout décrit ci-dessus), mais il ne **refuse pas** de connexions — associez-le à `checkin.max_connections` pour un plafond réel. Pas de clé `server.yaml` : définissez la variable d'environnement standard `GOMEMLIMIT` (dans docker-compose/systemd, à côté de la limite de mémoire du conteneur), ou `GOMEMLIMIT=off` pour aucune limite.
- **`grpc.gogc`** (par défaut `100` ; l'image de test de charge tourne à `200`). Plus élevé réduit le CPU du GC au prix d'un ensemble de travail de tas plus grand ; plus bas réduit les pics de RSS au prix de plus de CPU GC.
- **`checkin.interval`** (la cadence de sondage *côté agent*). C'est le nombre de connexions, pas le débit de requêtes, qui fixe le plafond mémoire — donc un intervalle plus long ne vous permet **pas** de maintenir plus d'envoys ; il abaisse seulement le débit de requêtes offert (intensité du CPU et du délestage en écriture). C'est toutefois précisément le levier qui vous fait passer de la colonne limitée par le CPU à celle limitée par la RAM.
- **`checkin.max_concurrent`** (par défaut `0` → mis à l'échelle selon le CPU, toujours actif). Ne le désactivez pas. Voir [Dimensionner `max_concurrent`](../../reference/server-yaml.md#sizing-max_concurrent).

## Quand fédérer (et comment)

Fédérez via spanner lorsque **l'une** de ces conditions est vraie pour la flotte prévue :

1. Le nombre d'envoys sur un seul hôte dépasserait le plafond sûr pour sa RAM (le tableau ci-dessus).
2. Des événements de check-in corrélés (vagues post-publication, ré-enrôlement massif, récupération après partition réseau) pourraient pousser les connexions simultanées au-delà du plafond sûr même si l'état stable tenait. Ces pics frappent directement la mémoire — dimensionnez pour le pic, pas pour la moyenne.
3. La tolérance aux pannes compte et vous ne pouvez pas perdre l'unique hôte sans perdre la flotte.

La configuration de spanner se trouve dans [Mettre en place spanner](set-up-spanner.md) ; la justification est l'ADR-026. Coût visible par l'opérateur : quelques pièces mobiles supplémentaires (bolts peer-equal, partitionnement par motif de nom d'hôte, gossip d'admissions multicast Tier-1 + REST Tier-2) en échange d'une mise à l'échelle linéaire et de la survie à la défaillance d'un seul hôte.

## Comment ces chiffres ont été mesurés

Les chiffres sont mesurés hors machine sur GCP (VM serveur + VM(s) de banc d'essai séparée(s), de sorte que la charge offerte est réelle et ne rivalise pas avec le serveur pour le CPU), à l'aide de [`test/fakefleet/`](../../../test/fakefleet/) pour enrôler des envoys synthétiques avec un inventaire split-traits réaliste de 150 KB et maintenir les check-ins à la cadence cible. Le CPU par check-in est `Δ process_cpu_seconds_total ÷ Δ taux de check-in` sur une fenêtre stabilisée de 60 s (en utilisant le taux signalé par le banc d'essai lui-même) ; la RAM par envoy est le **tas vivant après GC forcé** stabilisé (`pprof heap?gc=1`) ÷ nombre d'envoys enrôlés. La méthode complète, la matrice de runs et la provenance par chiffre vivent dans le registre : [`numbers-of-record.md`](../../internal.docs/numbers-of-record.md) et [`loadtest-per-cadence-runbook.md`](../../internal.docs/loadtest-per-cadence-runbook.md).

---
*Vérifié sur Vigo 0.76.35 · 2026-06-24. Ancrage : 8 vCPU / 32 GB mesuré ; lignes plus grandes calculées.*

*Confidentiel -- Alexander4, LLC. Ne pas redistribuer. Voir [licence](../../legal/license.md).*
