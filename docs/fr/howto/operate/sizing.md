<!-- vigo-i18n source_sha:4d332bf6eca2f8ac2c739549016d5d3e77569404029354e600539543a19723f3 -->

# Dimensionner un hôte vigosrv

À la fin de cette page, vous saurez **combien d'Envoys un serveur peut héberger confortablement**, ce qui se passe au-delà de cette limite, et les leviers opérateur (marge verticale, fédération horizontale) qui la déplacent.

**Quand l'utiliser :** avant de monter une nouvelle flotte, lors de la planification d'un renouvellement matériel, ou quand l'usage mémoire d'un serveur existant grimpe sous les vagues de convergence.

**Quand la sauter :** petites flottes — tout ce qui est en dessous de quelques milliers d'Envoys tourne bien en deçà du régime décrit ici, avec la config par défaut et n'importe quelle VM modeste.

> Tous les chiffres dérivent du registre de mesures du projet (la source unique de vérité). La ligne **8 vCPU / 32 Go** est l'ancrage mesuré ; le reste s'en déduit par mise à l'échelle.

## L'essentiel

**Deux plafonds — la mémoire, et à cadence rapide, le CPU — et lequel s'impose dépend entièrement de votre cadence de check-in.**

- Aux **cadences normales (30 s–5 m)** le serveur est **limité par la mémoire**. Budgétez **~1 Go de RAM serveur pour 1 000 Envoys** et vous êtes correctement dimensionné. C'est la limite contraignante pour la plupart des flottes.
- À **cadence agressive (≤ 5 s)** le **CPU** par check-in nécessaire pour vérifier et traiter chaque contact s'impose en premier. Budgétez **~1 vCPU pour 1 000 Envoys à une cadence de 1 s** (×N pour une cadence de N secondes — à 5 s c'est ~1 vCPU pour 5 000).
- Le **point de bascule est ≈ 4–6 s** : plus rapide → dimensionnez pour le CPU ; plus lent → dimensionnez pour la mémoire.

## Le tableau de dimensionnement

Nombre maximal d'Envoys par machine. Construit de manière **conservatrice** — la colonne CPU utilise le coût plus lourd du poll unaire (~0,80 ms/check-in) et la colonne RAM l'empreinte plus lourde du flux maintenu (~623 Ko/Envoy), si bien que vous êtes en sécurité quel que soit le chemin emprunté par vos agents. La marge est intégrée : CPU dimensionné à 75 % des cœurs, RAM à ~60 % du physique.

| Serveur (vCPU / RAM) | Agressive **≤ 5 s** (limité CPU, @1s) | Normale **30 s – 5 m** (limité RAM) | Base |
|---|---|---|---|
| 4 vCPU / 16 Go | ~3 700 | **~15 000** ⚠️ restez bien en deçà | mesuré (brownout) |
| **8 vCPU / 32 Go** | **~7 500** | **~30 000** | **ancrage mesuré** |
| 16 vCPU / 64 Go | ~15 000 | ~60 000 | calculé |
| 32 vCPU / 128 Go | ~30 000 | ~123 000 | calculé |
| 32 vCPU / 256 Go | ~30 000 | ~245 000 | calculé (RAM non vérifiée) |

**Comment l'utiliser :**

1. Choisissez votre cadence. La plupart des flottes tournent à 30 s–5 m → lisez la colonne **limitée RAM**.
2. Trouvez votre nombre d'Envoys → choisissez la plus petite machine dont le chiffre le dépasse.
3. Vous tournez à ≤ 5 s ? Vérifiez aussi la colonne CPU et prenez le **plus petit** des deux.

**Exemple :** 25 000 Envoys à 60 s → limité mémoire → **8 vCPU / 32 Go** (marge de 30 k). La *même* flotte à une cadence de 1 s est limitée CPU à ~7 500 sur cette machine, vous passeriez donc à **32 vCPU / 128 Go**.

## Règles empiriques (la voie rapide)

- **RAM : ~1 Go de RAM serveur pour 1 000 Envoys** (~623 Ko/Envoy de tas vif + base négligeable, maintenu sous ~60 % de la RAM physique). C'est généralement la limite contraignante.
- **Le CPU ne compte qu'à cadence rapide :** ~1 vCPU pour 1 000 Envoys à 1 s ; ~1 vCPU pour 5 000 à 5 s ; à 30 s+ le CPU est un non-sujet (il s'échelonne avec la cadence).
- **Point de bascule ≈ 4–6 s.** À 30 s / 5 m vous êtes toujours limité RAM — dimensionnez par la RAM.

## Mises en garde — lisez-les avant de vous fier à une ligne

- **⚠️ Les petites machines (≤ 16 Go) se dégradent, elles ne plantent pas.** À mesure qu'une machine 4/16 approche sa limite RAM, elle entre en **brownout GC** — sous le `GOMEMLIMIT` auto par défaut, elle dépense presque tout son CPU en ramasse-miettes pour respecter la limite souple, l'UI web / l'API / les métriques deviennent non réactives pendant ~1,5 m, et la machine n'atteint jamais un OOM-kill propre. Le plafond utile sur les petites machines est *l'apparition du brownout*, bien en deçà d'un chiffre linéaire naïf — **restez bien en dessous du nombre et ne faites pas tourner de petites machines près de la limite.** (Les plus grosses machines avec plus de cœurs atteignent un délestage/OOM propre à la place ; le couplage GC/CPU qui cause le brownout s'atténue au-delà de ~6–8 cœurs.)
- **Mesuré à ~150 Ko d'inventaire par Envoy.** Les flottes riches en paquets (~280 Ko — beaucoup de paquets installés, CVE, certificats) tournent ~20 % plus lourd en RAM → retranchez ~15–20 % des comptes limités RAM.
- **Les lignes 128–256 Go sont calculées, non mesurées** — la vérification RAM à grande échelle (250 k) est en attente. La ligne 8/32 est l'ancrage mesuré dont tout le reste se déduit.
- **Les flottes à flux maintenu** (le défaut — chaque Envoy en ligne maintient un AgentStream persistant) sont en réalité un peu *plus légères* que ce tableau conservateur sur le CPU (~0,56 ms contre 0,80 ms/check-in) et le tableau utilise déjà leur chiffre RAM plus lourd — vous avez donc de la marge, pas un déficit.

## Pourquoi le plafond est la mémoire par Envoy

Chaque Envoy connecté coûte **~623 Ko de tas serveur vif** : l'entrée FleetIndex en mémoire (~275 Ko — l'arbre de traits aplati et indexé), l'inventaire complet en cache qu'il a rapporté en dernier (~150 Ko pour une charge utile réaliste de 150 Ko), et la connexion AgentStream maintenue (~200 Ko — buffers de lecture/écriture gRPC, trois goroutines et leurs piles, état de flux et de contrôle de flux HTTP/2, marge GC). Un poller unaire non maintenu fait ~554 Ko — il économise ~69 Ko en ne maintenant pas le flux bidi. Le coût est linéaire et prévisible, ce qui explique pourquoi le dimensionnement est une simple multiplication : doublez la RAM de l'hôte, doublez le plafond d'Envoys.

## Réglages qui déplacent la limite

Ceux-ci vivent dans `server.yaml` (voir [server-yaml.md](../../reference/server-yaml.md)) et échangent une ressource contre une autre. Aucun ne transforme à lui seul un petit hôte en un grand hôte confortable — ils ajustent un hôte qui est proche de sa limite.

- **`checkin.max_connections`** (défaut `0` → auto-dérivé de la RAM de l'hôte ; `-1` → sans plafond). Plafonne les flux d'agent maintenus concurremment, refusant l'excédent avec `ResourceExhausted` (l'agent recule et réessaie) pour que le serveur déleste au lieu de grimper vers le mur. **Note :** le plafond auto-dérivé est construit à partir d'une estimation du buffer de connexion et est *optimiste* face au coût réel de ~623 Ko/Envoy conscient de l'inventaire — sur une machine limitée mémoire il peut se situer *au-dessus* du vrai plafond sûr, donc ne traitez pas le plafond seul comme votre limite ; dimensionnez par le tableau ci-dessus et fixez un nombre explicite si nécessaire. Journalisé au démarrage sous `held-stream admission cap auto-derived from host memory`.
- **`GOMEMLIMIT`** (limite souple de mémoire de Go). **Auto-dérivée à ~80 % du budget mémoire conscient du cgroup** — la marge de ~20 % est une marge de sécurité transitoire contre le dépassement GC. Elle échange du CPU contre la survie (et sur les petites machines ce compromis devient le brownout décrit ci-dessus), mais elle ne **refuse pas** de connexions — associez-la à `checkin.max_connections` pour un véritable plafond. Pas de clé `server.yaml` : fixez la variable d'environnement standard `GOMEMLIMIT` (dans docker-compose/systemd, à côté de la limite mémoire du conteneur), ou `GOMEMLIMIT=off` pour aucune limite.
- **`grpc.gogc`** (défaut `100` ; l'image de test de charge tourne à `200`). Plus élevé réduit le CPU GC au prix d'un working set de tas plus grand ; plus bas réduit les pics de RSS au prix de plus de CPU GC.
- **`checkin.interval`** (la cadence de poll *côté agent*). C'est le nombre de connexions, pas le débit de requêtes, qui fixe le plafond mémoire — donc un intervalle plus long ne vous permet **pas** de maintenir plus d'Envoys ; il abaisse seulement le débit de requêtes offert (intensité CPU et de délestage d'écriture). C'est en revanche exactement le levier qui vous fait passer de la colonne limitée CPU à la colonne limitée RAM.
- **`checkin.max_concurrent`** (défaut `0` → mis à l'échelle selon le CPU, toujours actif). Ne le désactivez pas. Voir [Dimensionner `max_concurrent`](../../reference/server-yaml.md#sizing-max_concurrent).

## Quand fédérer (et comment)

Fédérez via Spanner quand **l'une** de ces conditions est vraie pour la flotte planifiée :

1. Le nombre d'Envoys sur un seul hôte dépasserait le plafond sûr pour sa RAM (le tableau ci-dessus).
2. Des événements de check-in corrélés (vagues post-publication, ré-enrôlement massif, reprise après partition réseau) pourraient pousser les connexions concurrentes au-delà du plafond sûr même si l'état stationnaire tiendrait. Ces pics frappent la mémoire directement — dimensionnez pour le pic, pas pour la moyenne.
3. La tolérance aux pannes compte et vous ne pouvez pas perdre l'unique hôte sans perdre la flotte.

La configuration de Spanner est dans [Configurer Spanner](set-up-spanner.md) ; la justification est l'ADR-026. Coût visible par l'opérateur : quelques pièces mobiles supplémentaires (Bolts pairs-égaux, partitionnement par motif de nom d'hôte, gossip d'admissions multicast Tier-1 + REST Tier-2) en échange d'une mise à l'échelle horizontale linéaire et de la survie à une panne d'hôte unique.

## Comment ces chiffres ont été mesurés

Les chiffres sont mesurés hors-machine sur GCP (VM serveur + VM(s) de harnais séparée(s), pour que la charge offerte soit réelle et n'entre pas en concurrence avec le serveur pour le CPU), à l'aide de [`test/fakefleet/`](../../../test/fakefleet/) pour enrôler des Envoys synthétiques avec un inventaire réaliste de 150 Ko à traits scindés et maintenir les check-ins à la cadence cible. Le CPU par check-in est `Δ process_cpu_seconds_total ÷ Δ taux de check-in` sur une fenêtre stabilisée de 60 s (en utilisant le taux rapporté par le harnais lui-même) ; la RAM par Envoy est le **tas vif après GC forcé** stabilisé (`pprof heap?gc=1`) ÷ le nombre enrôlé. La méthode complète, la matrice d'exécution et la provenance par chiffre vivent dans le registre de mesures des mainteneurs, qui ne fait pas partie de la documentation publiée.

---
*Vérifié sur Vigo 0.76.35 · 2026-06-24. Ancrage : 8 vCPU / 32 Go mesuré ; lignes plus grandes calculées.*
