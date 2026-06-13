<!-- vigo-i18n source_sha:6048ec9a976c13bd52c5b7277ea254f2d87227611d86720316309ea298218b2b -->

# Configurer Scrier

À la fin de cette page, vous disposerez de sessions SSH/RDP/VNC, réservées aux administrateurs, depuis le navigateur et la CLI vers chaque Envoy enrôlé — sans ouvrir de port entrant sur aucun d'eux. Les octets transitent par la même connexion mTLS sortante que l'agent utilise déjà pour le check-in.

**Quand l'utiliser :** déboguer un Envoy précis sans bastion ni VPN ; accéder à distance à des bureaux via RDP/VNC ; atteindre des Envoys derrière les pare-feu client ou un CGNAT ; chaque fois que vous ouvririez sinon le port 22.

**Quand vous en passer :** si vous disposez déjà d'une configuration bastion/VPN fonctionnelle et de la traçabilité qu'elle apporte, Scrier est facultatif. Le compromis porte sur le fait de vouloir l'enregistrement de session et les clés éphémères par session (Scrier) plutôt que le bastion que vous avez déjà payé.

Scrier est la fonctionnalité d'accès distant administrateur de Vigo : sessions interactives SSH, RDP et VNC vers n'importe quel Envoy enrôlé, routées via le serveur Vigo et la connexion gRPC sortante déjà ouverte de l'Envoy. **Aucun port entrant n'est requis sur l'Envoy.** La même connexion que l'agent utilise pour le check-in transporte les octets de la session en retour.

<!-- TODO: capture screenshot of /scrier/<envoy_id>?protocol=ssh showing an active xterm.js session on Vigo 0.51.6 -->
*Capture d'écran requise : une session SSH Scrier active dans le navigateur, xterm.js entièrement rendu avec une invite de shell.*

## Ce que vous obtenez

- **SSH** depuis l'interface Web (xterm.js) et depuis `vigocli scrier ssh`.
- **RDP** depuis l'interface Web (Guacamole).
- **Observation de console VNC** depuis l'interface Web — voir ce qui s'affiche réellement à l'écran d'un utilisateur Linux, avec une invite de consentement facultative.
- Clés SSH éphémères par session (installées temporairement dans le fichier `authorized_keys` de l'utilisateur cible, supprimées à la fermeture de la session).
- Chaque session enregistrée dans la table `scrier_sessions` ; chaque connexion/déconnexion consignée dans le journal d'audit infalsifiable.

## L'activer dans `server.yaml`

```yaml
scrier:
  enabled: true
  guacd_addr: "127.0.0.1:4822"        # how vigosrv reaches guacd
  proxy_hostname: "127.0.0.1"         # how guacd reaches back to vigosrv
  allowed_ports:                      # ports the agent will tunnel to
    - 22
    - 3389
    - 5900
  max_sessions: 10                    # concurrent session cap
  recording_enabled: false            # schema placeholder; not yet writing
```

Le Docker Compose fourni place `vigo` et le sidecar `vigo-guacd` sur le réseau de l'hôte, de sorte qu'ils s'atteignent via la boucle locale de l'hôte — `guacd_addr: "127.0.0.1:4822"` et `proxy_hostname: "127.0.0.1"` comme indiqué. Les installations bare-metal (guacd en service local) utilisent les mêmes valeurs de boucle locale. Seul un guacd sur un hôte *séparé* nécessite des adresses différentes.

Redémarrez `vigosrv` après modification.

## Attribuer une clé publique SSH à chaque utilisateur web administrateur

Le chemin SSH de Scrier identifie l'opérateur par sa clé publique SSH. L'agent parcourt le fichier `~/.ssh/authorized_keys` de chaque utilisateur régulier sur l'Envoy cible et vous fait atterrir dans l'utilisateur qui détient votre clé. **Root n'est jamais une cible d'atterrissage Scrier** — utilisez `sudo` pour l'élévation.

La clé publique réside dans `users.ssh_public_key` et est définie de façon canonique au moment de la création de l'utilisateur. Pour les utilisateurs web administrateurs, elle est lue automatiquement depuis le répertoire personnel de l'utilisateur OS correspondant :

```bash
# Admin role: pubkey auto-read from /home/<username>/.ssh/id_ed25519.pub
# (fallback id_rsa.pub). --ssh-key-file is rejected — the OS-user mapping
# is the source of truth.
sudo vigocli webusers create --username dan --role admin
```

C'est l'étape d'amorçage canonique. Les sessions scrier du navigateur lisent cette colonne directement. Les sessions scrier de la CLI envoient ÉGALEMENT la clé publique en ligne à chaque invocation, de sorte que les rotations de clés fonctionnent pour la session CLI en cours sans réamorçage ; pour que le navigateur voie la nouvelle clé après rotation, supprimez puis recréez l'utilisateur web administrateur :

```bash
sudo vigocli webusers delete --username dan
sudo vigocli webusers create --username dan --role admin
```

La recréation relit depuis `/home/dan/.ssh/id_*.pub`. Après recréation, déconnectez-vous du navigateur puis reconnectez-vous — le cache de session conserve l'instantané de la clé publique pris au moment de la connexion.

Pour les utilisateurs viewer / compliance (sans accès scrier), le mappage utilisateur OS ne s'applique pas — ils peuvent être créés librement avec `--ssh-key-file <path>` si isowebauth a besoin d'une clé publique, mais ils ne sont pas des cibles Scrier.

## S'assurer que la clé publique de l'opérateur figure dans le fichier authorized_keys d'un utilisateur régulier sur chaque Envoy

C'est le seul état dont Scrier-SSH a besoin sur l'Envoy lui-même. La méthode conventionnelle consiste à déclarer une ressource `user` dans une usercrate qui répertorie la clé publique de l'opérateur dans `authorized_keys` :

```yaml
# /srv/vigo/stacks/usercrates/dan.vgo
resources:
  - name: dan
    type: user
    username: dan
    shell: /bin/bash
    state: present
    sudo_nopasswd: "true"
    authorized_keys: |
      ssh-ed25519 AAAA... operator@laptop
```

L'exécuteur user écrit ce fichier à chaque convergence et retire les lignes du marqueur éphémère de Scrier avant de comparer, de sorte que les clés de session transitoires ne déclenchent pas d'alertes de dérive.

## L'utiliser

**Depuis le navigateur :**

```
https://vigo:8443/scrier/<envoy_id>?protocol=ssh
https://vigo:8443/scrier/<envoy_id>?protocol=rdp
https://vigo:8443/scrier/<envoy_id>?protocol=vnc
```

Réservé aux administrateurs. xterm.js pour SSH, client Guacamole pour RDP/VNC.

![Une session SSH Scrier en direct dans le navigateur, atterrie sur un Envoy managé](../../img/screenshots/vigo.scrier-ssh.png)

La session vous fait atterrir dans l'utilisateur OS dont le fichier `authorized_keys` contient la clé publique de votre utilisateur web (ici, `dan@girlslaptop`) — jamais root. Notez le `from 127.0.0.1` sur la ligne de connexion : l'agent relaie la connexion via son flux serveur existant vers un écouteur en boucle locale uniquement, de sorte que l'Envoy n'ouvre jamais de port entrant.

**Depuis le terminal :**

```bash
vigocli scrier ssh annlap                   # by hostname
vigocli scrier ssh 3c5a7f2b-0000-...        # by envoy UUID
vigocli scrier ssh annlap --ssh-key-file ~/.ssh/id_other.pub   # override auto-detect
```

## Fonctionnement

```mermaid
sequenceDiagram
    autonumber
    participant Op as Operator
    participant Srv as Vigo server
    participant Agt as Agent on envoy
    participant Sd as sshd on envoy (127.0.0.1:22)

    Op->>Srv: POST /api/v1/scrier/connect<br/>(envoy_id, protocol, ssh_public_key?)
    Note right of Srv: validate pubkey,<br/>write-through users.ssh_public_key
    Srv->>Agt: TunnelRequest (over existing AgentStream)
    Agt->>Srv: open TunnelStream gRPC
    Srv->>Agt: TunnelOpen<br/>(verify_pubkey, ephemeral_pubkey)
    Note right of Agt: scan /home/*/.ssh/authorized_keys<br/>for verify_pubkey<br/>(getpwent, uid≠0)
    Agt->>Agt: install ephemeral_pubkey in matched user<br/>(# vigo-scrier-ephemeral marker)
    Agt->>Srv: TunnelOpenResult (ssh_username = <matched user>)
    Srv->>Sd: SSH dial 127.0.0.1:22 with ephemeral_privkey,<br/>auth as <matched user> (via tunnel)
    Op->>Srv: WebSocket /ws/scrier/{session_id}
    Srv-->>Op: raw PTY bytes ↔ Sd via tunnel
    Note over Agt: on close: strip ephemeral line<br/>(RAII; also a startup-sweep<br/>safety net)
```



1. L'opérateur appelle `POST /api/v1/scrier/connect` avec `{envoy_id, protocol, port, ssh_public_key (optional)}`. La CLI renseigne `ssh_public_key` par découverte locale ; le navigateur le laisse vide.
2. Le serveur crée une ligne `scrier_sessions` à l'état `waiting` et pousse un `TunnelRequest` vers l'Envoy via son `AgentStream` déjà ouvert — la seule étape initiée par le serveur.
3. L'agent ouvre une nouvelle connexion gRPC `TunnelStream` vers le serveur. Le serveur répond par `TunnelOpen` : clé publique de la cible SSH + clé publique éphémère par session pour SSH, ou `host:port` cible pour RDP/VNC.
4. Pour SSH, l'agent parcourt les utilisateurs réguliers via `getpwent`, trouve l'utilisateur dont le fichier `authorized_keys` contient la clé publique de l'opérateur, ajoute la clé éphémère avec un marqueur `# vigo-scrier-ephemeral`, et renvoie le nom d'utilisateur OS. Le serveur établit une connexion SSH vers `127.0.0.1:22` via le tunnel à l'aide de la clé privée éphémère.
5. Pour RDP/VNC, l'agent se connecte à `127.0.0.1:<port>` sur l'Envoy ; Guacamole négocie côté serveur et relaie les instructions d'affichage sur le WebSocket de l'opérateur sous forme de trames binaires.
6. Le client de l'opérateur (xterm.js, Guacamole.js, ou le lecteur WebSocket de la CLI) s'attache à `/ws/scrier/{session_id}` et les octets circulent.
7. La fermeture du WebSocket par l'une ou l'autre partie ferme le tunnel. L'agent retire sa ligne éphémère de `authorized_keys` — le nettoyage s'exécute même si l'agent plante en cours de session, et un balayage au démarrage de l'agent supprime toute ligne périmée laissée par des plantages précédents. La ligne de session est marquée `closed` et un enregistrement d'audit `scrier.disconnect` est écrit.

## Modes de défaillance courants

| Symptôme | Cause | Correctif |
|---|---|---|
| La session échoue avec `SSH pubkey not authorized on this envoy` | La clé publique de l'opérateur ne figure dans le fichier `authorized_keys` d'aucun utilisateur régulier sur la cible. | Ajoutez-la au champ `authorized_keys` d'une usercrate et republiez ; ou faites un `ssh-copy-id` une fois et laissez l'exécuteur user la récupérer. |
| La session échoue avec `no SSH pubkey available` | `users.ssh_public_key` est vide ET la requête de connexion n'avait pas de champ `ssh_public_key`. | Exécutez `vigocli scrier ssh <envoy>` une fois depuis un hôte ayant votre clé SSH sur disque ; ou `PUT /api/v1/users/{id}` avec `ssh_public_key`. |
| La session échoue avec `agent did not open TunnelStream within 90s` | L'Envoy ne répond pas au signal de réveil. Cause fréquente : pression disque ou agent bloqué. | Vérifiez la péremption avec `vigocli envoys list` ; vérifiez le disque hôte de l'agent via `vigocli traits livequery host_self.fs_root_pct`. |
| RDP / VNC fonctionne mais pas SSH | L'infrastructure de tunnel est correcte ; la rupture est dans l'authentification propre à SSH. | Presque toujours l'un des deux cas SSH ci-dessus. Inspectez les journaux du serveur autour de `scrier session state`. |

## Limites

- Réservé aux administrateurs. Les rôles `compliance` et `viewer` ne peuvent pas initier de sessions Scrier.
- L'enregistrement de session est un espace réservé de schéma ; rien n'écrit encore réellement d'enregistrements.
- Pas de SCP / transfert de fichiers. Shell interactif uniquement.
- Le VNC Wayland n'est pas pris en charge (pas de serveur VNC natif Wayland fiable à l'heure de la rédaction).
- Le VNC Windows (TightVNC) est sur la feuille de route, pas livré.
- La CLI nécessite un TTY — les invocations redirigées sont refusées.

## Et ensuite

- **Une session échoue** → [Résoudre les problèmes courants — section Scrier](troubleshoot.md#ssh-pubkey-not-authorized-on-this-envoy) couvre les quatre chaînes d'échec les plus courantes avec leurs correctifs.
- **Vous voulez des preuves de conformité sur qui s'est connecté en SSH à quoi et quand** → chaque connexion / déconnexion Scrier est consignée dans le [journal d'audit chaîné par hachage](../compliance/compliance-reporting.md) ; exportez-le via `vigocli audit export`.
- **Vous voulez l'enregistrement de session** → non livré aujourd'hui ; le commutateur `recording_enabled` est un espace réservé de schéma pour la fonctionnalité à venir.

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*

*Confidentiel — Alexander4, LLC. Reproduction interdite. Voir [../legal/license.md](../../legal/license.md).*
