<!-- vigo-i18n source_sha:5c7073a7ecb30273328b36822098077de1a93ec6e6760e14aabe2c3b9652f2f9 -->

# Dimensionner un hôte vigosrv

À la fin de cette page, vous saurez **combien d'envoys un seul serveur peut héberger confortablement**, ce qui se passe au-delà de cette limite, et les leviers opérateur (marge verticale, fédération horizontale) qui la déplacent.

**Quand l'utiliser :** avant de monter une nouvelle flotte, lors de la planification d'un renouvellement matériel, ou quand l'usage mémoire d'un serveur existant grimpe sous les vagues de convergence.

**Quand la sauter :** petites flottes — tout ce qui est en dessous de quelques milliers d'envoys fonctionne bien en deçà du régime traité par cette page, avec la configuration par défaut et n'importe quelle VM modeste.

## L'essentiel

**Aux cadences de check-in typiques, le plafond mono-hôte de vigosrv est la mémoire, et il est linéaire : environ 220 Ko de RAM par envoy connecté.** Le modèle connexion/goroutine est plat et prévisible, et la latence se dégrade gracieusement sous surcharge plutôt que de s'effondrer. **Par défaut, le serveur dimensionne un plafond de connexions maintenues à partir de la RAM de l'hôte et, une fois ce plafond sûr atteint, écarte les nouveaux flux avec `ResourceExhausted` — l'agent se met en retrait et réessaie — de sorte qu'il reste debout sous surcharge plutôt que d'être tué par l'OOM.** Atteindre le point d'OOM du noyau exige de désactiver explicitement le plafond (`checkin.max_connections: -1`) ; les chiffres ci-dessous sont le comportement *physique* d'un serveur sans plafond, c'est-à-dire ce que le plafond est dimensionné pour vous éviter de dépasser.

> **Deux plafonds — la mémoire, et à cadence rapide + grand inventaire, le CPU.** Le plafond mémoire de cette page est le plafond contraignant pour la plupart des flottes. Mais à une cadence de check-in agressive de **1 s** avec un **grand inventaire réaliste** (~150 Ko+ de paquets, certificats, etc.), le CPU par check-in pour reconstruire et traiter les traits sature en *premier* : mesuré hors-machine à **~7 450 envoys sur 8 vCPU** avec le mode par défaut `checkin.stream_signature: all` (chaque check-in vérifié ed25519), ou **~8 500** avec `handshake-only` (contre le plafond mémoire de ~60k). Le plafond CPU évolue ≈ linéairement avec les cœurs **et** avec la cadence — à une cadence de 30 s il est ~30× plus élevé, repassant au-dessus du plafond mémoire — donc à moins de faire des check-ins à 1 s sur une flotte volumineuse et riche en inventaire, la mémoire reste la limite. **Règle empirique :** cadence 1 s → dimensionnez pour le CPU (~7 450 envoys par 8 vCPU au mode de signature par défaut, ×N pour une cadence de N secondes ; +~14 % avec handshake-only) ; cadence plus lente → dimensionnez pour la mémoire (ci-dessous).

Sur le matériel de cette page (8 CPU logiques, **31 Gio de RAM**, NVMe), mesuré en direct :

| Envoys simultanés | RSS serveur | Résultat |
|---|---|---|
| 40 000 | 8,4 Go | confortable |
| 50 000 | 11,2 Go | confortable |
| **60 000** | **13,4 Go** | **confortable — plafond sûr recommandé pour 31 Gio** |
| ~70 000 | ~14,7 Go | **tué par l'OOM** |

Le plafond sûr se situe à ~60k parce que vous voulez une marge sous le point d'OOM pour le cache de pages, l'agent `vigo` colocalisé, le working set du GC, et une marge pour les rafales de check-in corrélées. Le mur dur sur cette machine est ~70k.

**La règle de dimensionnement : prévoyez ~220 Ko de RAM par envoy** (≈ 256 Mo par 1 000 envoys), plus ~1,5 Go de base, et gardez le working set des connexions sous ~50 % de la RAM de l'hôte. Cela évolue linéairement avec la mémoire :

| RAM de l'hôte | Plafond sûr | Mur dur (OOM) |
|---|---|---|
| 16 Gio | ~28 000 | ~33 000 |
| 32 Gio | ~60 000 | ~70 000 |
| 64 Gio | ~120 000 | ~140 000 |
| 128 Gio | ~260 000 | ~290 000 |

La colonne « plafond sûr » est ce que `checkin.max_connections: 0` dérive automatiquement (40 % de la RAM ÷ 220 Ko) ; la ligne 32 Gio est l'ancrage mesuré (60k confortable / 70k OOM), les autres en découlent linéairement. L'auto-dérivation et ces chiffres concordent à l'arrondi près.

**Au-delà du plafond sûr, la réponse est la fédération, pas seulement une machine plus grosse** (même si une machine plus grosse achète une marge proportionnelle). Le mode Spanner de Vigo (ADR-026) partitionne la flotte par motif de nom d'hôte entre des Bolts pairs-égaux ; chaque Bolt détient sa propre tranche et ils diffusent en CRDT le registre des admissions. C'est le chemin pris en charge au-delà d'un seul hôte, et c'est aussi ce qui vous offre la survie à une panne mono-hôte.

> **`checkin.max_connections` est activé par défaut — auto-dérivé de la RAM de l'hôte.** Au-delà de ce nombre de flux d'agent maintenus simultanément, le serveur refuse les nouveaux avec `ResourceExhausted` (l'agent se met en retrait et réessaie) au lieu d'accepter des connexions jusqu'à ce que le noyau le tue par OOM. Un kill OOM est une panne dure — il coupe toutes les connexions en vol et perd des enregistrements de run en pleine transaction ; `restart: unless-stopped` récupère en ~60 s mais l'écartement est le chemin gracieux. Laissé non défini (`0`), le plafond se résout au démarrage au plafond sûr de l'hôte (≈ 40 % de la RAM ÷ 220 Ko — par exemple ~60 000 sur un hôte de 31 Gio), journalisé comme `held-stream admission cap auto-derived from host memory`. Remplacez-le par un nombre explicite si vous avez mesuré une autre limite pour votre matériel ; mettez `-1` pour le désactiver entièrement et accepter le comportement historique d'OOM-au-mur. Surveillez `vigo_streams_active` par rapport à la valeur effective. Le plafond protège un hôte ; une flotte qui le dépasse légitimement est le signal d'ajouter de la RAM ou de [fédérer](#quand-fédérer-et-comment).

## À quoi ressemble « confortable », et sur quel matériel

Les chiffres ci-dessus sont mesurés en direct, pas extrapolés. Le harnais est [`test/burstwave/`](../../../test/burstwave/) (un harnais de rafale coordonné chemin-lecture + chemin-écriture qui exerce le cycle d'agent réel) ; le wrapper de capture est [`test/burstwave/capture.sh`](../../../test/burstwave/capture.sh) (pprof CPU + goroutines + RSS + télémétrie FD aux côtés du harnais) ; l'analyseur par run est [`test/burstwave/report.py`](../../../test/burstwave/report.py).

**Matériel de l'hôte serveur :**

| | |
|---|---|
| CPU | Intel i5-1155G7 — 4 cœurs, 8 CPU logiques (SMT-2), 2,5 GHz de base / 4,5 GHz en boost |
| Mémoire | 31 Gio DDR4 |
| Stockage | SSD NVMe (Crucial P3), ext4 |
| OS / noyau | Linux Mint 22, noyau 6.8 |
| Conteneur | Docker, **réseau hôte**, `nofile` du conteneur relevé à 1 048 576, aucune limite cgroup CPU/mémoire |
| Version Vigo | 0.69.20 |

**Mise à l'échelle mesurée (à l'intervalle de check-in par défaut de 1 seconde, `--results-per-report 12`, connexions pilotées depuis plusieurs hôtes pour que la charge offerte soit réelle — voir la note de méthodologie plus bas) :**

| Envoys simultanés | Pic de goroutines | Pic de RSS | Check-in `p50` / `p99` | Pic CPU hôte | Verdict |
|---|---|---|---|---|---|
| 40 000 | 122 000 | 8,4 Go | 1,5 s / 3,0 s | 677 % | confortable |
| 50 000 | 152 000 | 11,2 Go | 3,0 s / 6,1 s | 619 % | confortable |
| 60 000 | 182 000 | 13,4 Go | 3,0 s / 24 s | 645 % | confortable (plafond sûr) |
| ~70 000 | (≈210 000 projeté) | ~14,7 Go | — | ~650 % | **tué par l'OOM** |

Trois choses que la matrice montre :

- **La mémoire est la seule limite qui contraint.** Le RSS suit le nombre de connexions à un rythme parfaitement linéaire de ~220 Ko/conn sans fuite (les goroutines sont exactement 3 par connexion — un lecteur, un écrivain, un handler — donc la colonne goroutines n'est que `3 × envoys + surcharge`). La ligne 60k était une prédiction pré-enregistrée : 13,2 Go prédits, 13,4 Go mesurés.
- **Le CPU n'est jamais le facteur limitant.** Le pic CPU de l'hôte est resté autour de 620–680 % des 800 % disponibles (8 cœurs logiques) en tout point, jusqu'à l'OOM inclus. Vous manquerez de RAM bien avant de manquer de CPU.
- **La latence se dégrade gracieusement, elle ne s'effondre pas.** Sous une forte surcharge à intervalle de 1 s, le `p50` du check-in plafonne autour de 3 s (le chemin d'écriture écarte l'excès avec `ResourceExhausted` ; les lectures restent servies). Le serveur atteint un équilibre de contre-pression stable plutôt que de s'emballer — jusqu'à ce qu'il atteigne le mur de la RAM.

Le `ResourceExhausted` que vous voyez dans les compteurs d'écartement est le plafond d'admission `checkin.max_concurrent` / ReportResult, toujours actif, qui fait son travail : il borne la concurrence en *écriture* face au rédacteur SQLite unique et écarte proprement (l'agent se met en retrait). Ce plafond est sain et n'est **pas** le facteur limitant en mémoire — il protège le rédacteur, pas la RAM du processus.

## Pourquoi le plafond est la mémoire par connexion

Chaque envoy connecté coûte ~220 Ko de mémoire résidente : les tampons de lecture+écriture par connexion par défaut de gRPC (~64 Kio), les trois goroutines et leurs piles, l'état des flux HTTP/2 et du contrôle de flux, et la marge GC sur l'ensemble. C'est linéaire et prévisible — c'est exactement pourquoi le modèle est une simple multiplication. Doublez la RAM de l'hôte, doublez le plafond d'envoys.

Les builds Vigo antérieurs (≤ 0.69.19) montraient un plafond d'*instabilité du transport* autour de 20k connexions — des tempêtes d'`Unavailable` bien en dessous du mur mémoire. C'était un `KeepaliveTimeout` par défaut trop agressif de 10 s qui tuait des connexions saines sous la profondeur de file de l'ordonnanceur ; il a été relevé à 30 s en 0.69.20 (voir l'historique des versions). Une fois cela corrigé, il n'y a aucun plafond de transport en dessous du mur mémoire — le serveur met les connexions à l'échelle linéairement jusqu'à manquer de RAM.

## Réglages qui déplacent la limite

Ceux-ci vivent dans `server.yaml` (voir [server-yaml.md](../../reference/server-yaml.md)) et échangent une ressource contre une autre. Aucun ne transforme à lui seul un hôte de 31 Gio en un hôte 100k confortable — ils ajustent un hôte proche de sa limite.

- **`checkin.max_connections`** (défaut `0` → auto-dérivé de la RAM de l'hôte ; `-1` → sans plafond). Le garde-fou direct contre le mur mémoire : plafonne les flux d'agent maintenus simultanément, refusant l'excès avec `ResourceExhausted` pour que le serveur écarte au lieu de subir l'OOM. Activé par défaut — `0` se résout au démarrage à ≈ 40 % de la RAM ÷ 220 Ko (le tableau du plafond sûr ci-dessus) ; mettez un nombre explicite pour le remplacer, ou `-1` pour le désactiver. C'est ce qui transforme « OOM au mur » en « retrait gracieux au mur » — l'auto-dérivation de `GOMEMLIMIT` ci-dessous ne fait que ralentir la montée ; elle ne refuse pas de connexions.
- **`GOMEMLIMIT`** (la limite mémoire souple de Go). **Auto-dérivé à ~80 % du budget mémoire conscient des cgroups** — la marge de ~20 % est une réserve de sécurité pour le dépassement transitoire du GC (le tas Go peut dépasser la limite avant que le GC ne rattrape sous une rafale de charge, par ex. la réhydratation d'une grande flotte au redémarrage ; sans la limite c'est un OOM-kill au lieu d'un GC dur près de la limite). Le RSS hors-tas-Go que `GOMEMLIMIT` ne peut pas plafonner est négligeable — mesuré à ~40 Mo sur une flotte de 2 500 envoys. Il échange du CPU contre de la survie, mais il ne cesse pas d'accepter des connexions — associez-le à `checkin.max_connections` pour un véritable plafond. Il n'y a pas de clé `server.yaml` : définissez la variable d'environnement standard `GOMEMLIMIT` (dans docker-compose/systemd, à côté de la limite mémoire du conteneur) pour le remplacer, ou `GOMEMLIMIT=off` pour aucune limite.
- **`grpc.gogc`** (défaut `100`). Plus bas (par ex. `50`) réduit le working set du GC et les pics de RSS au prix de plus de CPU GC ; plus haut fait le compromis inverse.
- **`grpc.max_concurrent_streams`** (défaut `5000`). Borne l'état des flux HTTP/2 par connexion quand un envoy se comporte mal ; ne change pas le nombre d'envoys.
- **`checkin.interval`** (la cadence de polling *côté agent*). C'est le nombre de connexions, pas le débit de requêtes, qui fixe le plafond mémoire — donc l'intervalle ne déplace **pas** la limite mémoire. Il fixe le débit de requêtes offert (intensité de l'écartement en écriture et latence sous charge), pas le nombre d'envoys que vous pouvez tenir. Un intervalle plus long rend un hôte proche de son plafond plus calme ; il ne vous permet pas de tenir plus d'envoys.
- **`checkin.max_concurrent`** (défaut `0` → mis à l'échelle selon le CPU, toujours actif). Ne le désactivez pas. Voir [Dimensionner `max_concurrent`](../../reference/server-yaml.md#sizing-max_concurrent).

## Quand fédérer (et comment)

Fédérez via Spanner dès que **l'une** de ces conditions est vraie pour la flotte prévue :

1. Le nombre d'envoys sur un hôte dépasserait le plafond sûr pour sa RAM (le working set `~220 Ko × envoys` franchissant ~50 % de la mémoire de l'hôte — ~60k sur 31 Gio, évoluant linéairement avec la RAM).
2. Des événements de check-in corrélés (vagues post-publication, ré-enrôlement de masse, récupération après partition réseau) pourraient pousser les connexions simultanées au-delà du plafond sûr même si le régime permanent tiendrait. Ces pics sont réels et ils retombent directement sur la mémoire — dimensionnez pour le pic, pas pour la moyenne.
3. La tolérance aux pannes compte et vous ne pouvez pas perdre l'hôte unique sans perdre la flotte.

La configuration de Spanner est dans [Configurer Spanner](set-up-spanner.md) ; la justification est l'ADR-026. Coût visible par l'opérateur : quelques pièces mobiles supplémentaires (Bolts pairs-égaux, partitionnement par motif de nom d'hôte, diffusion des admissions en multicast Tier-1 + REST Tier-2) en échange d'une mise à l'échelle linéaire et de la survie à une panne mono-hôte.

## Refaire les mesures vous-même

**La méthodologie compte ici — les tests de charge naïfs mesurent le harnais, pas le serveur.** Pour obtenir un vrai chiffre serveur, vous devez lever quatre limites côté client et côté déploiement, sinon vous atteindrez un faux plafond bien avant le vrai :

1. **Lancez le serveur avec le réseau hôte** (le compose fourni le fait). Le réseau en pont route chaque connexion à travers un unique processus userland `docker-proxy` par port qui devient le goulot d'étranglement bien avant le serveur.
2. **Relevez le `nofile` du conteneur** (le compose fourni met 1 048 576). La valeur par défaut de 65 536 plafonne le serveur à ~63k connexions — un mur de descripteurs de fichiers, pas une vraie limite.
3. **Pilotez la charge depuis plusieurs hôtes.** Un seul hôte client composant un unique `host:port` partage un seul pool de ~28k ports éphémères (`net.ipv4.ip_local_port_range`), donc une seule machine ne peut pas *offrir* plus de ~28k connexions, quelle que soit la taille du serveur. Élargissez la plage (`sysctl -w net.ipv4.ip_local_port_range="10000 65535"` → ~55k) et répartissez la charge sur plusieurs machines pilotes, synchronisées avec `--start-at`.
4. **Ne pilotez pas la charge depuis l'hôte serveur lui-même** au-delà d'une référence en loopback — le générateur de charge entre en concurrence avec vigosrv pour le CPU et fausse le résultat.

```bash
# build the harness once (per driver host; or scp the binary)
go build -o /tmp/burstwave ./test/burstwave

# one driver, server on loopback, with full server-side capture.
# --second-count 1 + --second-burst-at 1.0 keeps cohort B effectively disabled
# (one envoy released at end of A's window), so this is a clean single-cohort
# steady-state measurement. Drop those two flags to test the overlap regime.
test/burstwave/capture.sh --output /tmp/sizing-run -- \
  --server 127.0.0.1:1530 --api https://127.0.0.1:8443 \
  --admin-token "$(sudo cat /srv/vigo/cli-admin-token)" \
  --count 30000 --second-count 1 --interval 1s --results-per-report 12 \
  --warmup 30 --duration 300 --second-burst-at 1.0 \
  --server-container vigo

# additional driver hosts (over the LAN) join the same instant via --start-at:
/tmp/burstwave --server <server-ip>:1530 --api https://<server-ip>:8443 \
  --admin-token "<token>" --count 30000 --second-count 1 --interval 1s \
  --warmup 30 --duration 300 --second-burst-at 1.0 \
  --hostname-prefix wave-b- --start-at 2026-01-01T00:00:00Z --output /tmp/sizing-run-b
```

Le wrapper de capture produit `cpu.pprof`, `goroutines-peak.txt`, `heap-peak.pprof`, `resources.csv` (goroutines + RSS + FD toutes les 5 s), `stats.csv` (docker stats toutes les 1 s) et `run.log`. Donnez le répertoire de run à `test/burstwave/report.py <dir>` pour un verdict en langage clair (il renomme les codes gRPC, sépare la surcharge de démarrage du régime permanent, et signale si un run a atteint une limite du *harnais* plutôt que du serveur). Analysez le profil CPU avec `go tool pprof -http=:8080 /tmp/sizing-run/cpu.pprof`.

---
*Vérifié sur Vigo 0.69.20 · 2026-05-30.*

*Confidentiel -- Alexander4, LLC. Ne pas redistribuer. Voir [licence](../../legal/license.md).*
