<!-- vigo-i18n source_sha:907a57a2403a76e2799a73de1cb2e5daf8dd82a28bf459387add02af820db078 -->


# Configurer des équipements réseau

Au terme de cette page, vous aurez des commutateurs, des routeurs et des pare-feux sous le même modèle de convergence que vos serveurs — chaque équipement apparaît dans `vigocli envoys list` avec ses traits, son statut de conformité et son historique d'exécutions. L'astuce : un **Envoy passerelle** désigné (un hôte Linux/macOS ordinaire exécutant l'agent) effectue le travail SSH pour le compte de Vigo, de sorte que l'équipement réseau lui-même n'exécute aucun logiciel Vigo. Cinq familles d'équipements aujourd'hui : Cisco IOS, NX-OS, IOS-XR, Arista EOS, Juniper Junos.

**Quand l'utiliser :** vous avez des équipements qui ne peuvent (ou ne doivent) pas exécuter le binaire de l'agent mais qui doivent figurer sur les mêmes rails de conformité, d'audit et de convergence que le reste du parc. Le cas classique est un ensemble de commutateurs d'accès dont vous voulez rendre déclaratif l'état des VLAN / interfaces / ACL.

**Quand l'éviter :** les équipements qui exécutent déjà l'agent (routez-les comme des Envoys normaux) ; les équipements ponctuels que vous préférez gérer avec un outil spécialisé (NSO, NAPALM, contrôleurs constructeur) ; l'inventaire en lecture seule (utilisez plutôt le chemin de requête d'inventaire — inutile d'introduire une passerelle).

## L'activer

Choisissez un Envoy sur le même réseau de gestion que le ou les équipements — appelez-le l'**Envoy passerelle**. Il établira une connexion SSH vers chaque équipement réseau pour le compte de Vigo. Déclarez les équipements en tant qu'Envoys via la forme de hostcrate d'équipement réseau, en précisant la passerelle :

```yaml
# /srv/vigo/stacks/envoys.vgo
envoys:
  - match: "switch-*.lab"
    network_device:
      gateway: gateway-envoy.lab
      type: cisco_ios
      ssh_user: "secret:vigo/net/ssh-user"
      ssh_password: "secret:vigo/net/ssh-pass"
    configcrates: [switch-baseline]
```

L'Envoy passerelle lui-même a besoin que la capacité `network_device` correspondante soit activée dans `server.yaml` (ou via le rôle de l'Envoy passerelle).

## L'utiliser

Une fois le hostcrate déployé et l'Envoy passerelle convergé, les équipements réseau apparaissent dans `vigocli envoys list` comme n'importe quel autre Envoy. Les configcrates qui les ciblent exécutent leurs ressources via la passerelle : par exemple, une ressource `cisco_vlan` déclare « le VLAN 100 doit exister sur ce commutateur » ; la passerelle s'y connecte en SSH, vérifie l'état actuel, applique le différentiel et rend compte.

## Architecture

```
                    +-----------+
                    |  vigosrv     |
                    |  server   |
                    +-----+-----+
                          | gRPC/mTLS
                    +-----+-----+
                    | Gateway   |    SSH
                    | Envoy    +-----------> [Cisco Switch]
                    | (agent)   +-----------> [Juniper Router]
                    +-----------+-----------> [More devices...]
```

Un **Envoy passerelle** est un serveur Linux/macOS normal exécutant l'agent Vigo. Il agit comme un mandataire : à chaque check-in, le serveur envoie à la passerelle sa propre politique *plus* une liste d'entrées `ProxiedDevice` pour tous les équipements réseau qui lui sont affectés. La passerelle :

1. Se connecte à chaque équipement en SSH
2. Collecte les traits de l'équipement (modèle, micrologiciel, interfaces, VLAN, routes)
3. Exécute la convergence à l'aide d'exécuteurs adossés à SSH
4. Renvoie les résultats et les traits au serveur sous l'ID d'Envoy virtuel de chaque équipement

## Configuration

Les équipements réseau sont définis dans `envoys.vgo` aux côtés des Envoys ordinaires. Une entrée d'équipement comporte un champ `gateway:` pointant vers l'Envoy gestionnaire et un champ `device_type:`.

```yaml
envoys:
  # Gateway -- normal Linux server
  - match: "netmgmt01.example.com"
    roles: [bastion]

  # Cisco core switch
  - match: "core-sw01.example.com"
    device_type: cisco_ios
    gateway: "netmgmt01.example.com"
    credential: "secret:network/core-sw01/ssh"
    username: admin
    ip_address: "10.0.1.1"
    configcrates: [switch-base, switch-vlans]

  # Juniper edge router
  - match: "edge-rtr01.example.com"
    device_type: juniper_junos
    gateway: "netmgmt01.example.com"
    credential: "secret:network/edge-rtr01/ssh"
    username: admin
    ip_address: "10.0.1.2"
    configcrates: [router-base, bgp-peering]
```

### Champs d'une entrée d'équipement

| Champ | Requis | Défaut | Description |
|-------|----------|---------|-------------|

| `match` | Oui | -- | Nom d'hôte de l'équipement (utilisé pour l'identification dans le parc) |
| `device_type` | Oui | -- | `cisco_ios`, `cisco_nxos` ou `juniper_junos` |
| `gateway` | Oui | -- | Nom d'hôte de l'Envoy passerelle qui gère cet équipement |
| `credential` | Oui | -- | Identifiant SSH utilisant le préfixe `secret:` |
| `username` | Non | `admin` | Nom d'utilisateur SSH |
| `port` | Non | `22` | Port SSH |
| `ip_address` | Non | -- | IP de gestion (si différente du nom d'hôte DNS) |
| `configcrates` | Non | -- | Liste de configcrates à appliquer |
| `roles` | Non | -- | Liste de rôles à appliquer |

### Règles de validation

- `device_type` et `gateway` doivent être définis ensemble
- `gateway` doit référencer un Envoy existant qui n'est **pas** lui-même un équipement
- `credential` doit utiliser le préfixe `secret:`
- `device_type` doit être l'un de : `cisco_ios`, `cisco_nxos`, `cisco_iosxr`, `arista_eos`, `juniper_junos`

## Types d'équipements pris en charge

| Type d'équipement | Plateforme | Mode de config | Versions testées |
|-------------|----------|-------------|-----------------|
| `cisco_ios` | Cisco IOS/IOS-XE | CLI (configure terminal) | 15.x, 16.x, 17.x |
| `cisco_nxos` | Cisco NX-OS | CLI (configure terminal) | 9.x, 10.x |
| `cisco_iosxr` | Cisco IOS-XR | CLI (configure terminal/commit) | 7.x, 24.x |
| `arista_eos` | Arista EOS | CLI (configure terminal) | 4.28+, 4.30+ |
| `juniper_junos` | Juniper Junos | CLI (configure/commit) | 20.x, 21.x, 22.x |

## Authentification SSH

Les équipements s'authentifient par mot de passe ou par clé SSH, déterminé automatiquement :

- Si l'identifiant contient `PRIVATE KEY`, l'authentification par clé est utilisée
- Sinon, l'authentification par mot de passe est utilisée

Les clés d'hôte sont vérifiées selon le principe Trust On First Use (TOFU) : la première connexion accepte et met en cache la clé d'hôte de l'équipement. Les connexions suivantes vérifient par rapport à la clé mise en cache. Un écart constitue une erreur bloquante (possible attaque de l'homme du milieu).

## Exécuteurs réseau

Deux exécuteurs au niveau du transport gèrent des opérations arbitraires sur les équipements :

| Exécuteur | Description |
|----------|-------------|
| [`ssh_exec`](../../reference/vigo/resources/) | Exécuter des commandes sur les équipements (équivalent d'`exec`) |
| [`network_config`](../../reference/vigo/resources/) | Gestion déclarative de blocs de configuration |

Quatorze exécuteurs spécifiques aux équipements offrent une gestion structurée et idempotente :

| Exécuteur | Description |
|----------|-------------|
| [`cisco_vlan`](../../reference/vigo/resources/) | Gérer les VLAN sur les commutateurs Cisco |
| [`cisco_interface`](../../reference/vigo/resources/) | Gérer la configuration d'interface Cisco |
| [`cisco_acl`](../../reference/vigo/resources/) | Gérer les listes de contrôle d'accès Cisco |
| [`junos_interface`](../../reference/vigo/resources/) | Gérer la configuration d'interface Junos |
| [`network_ntp`](../../reference/vigo/resources/) | Configuration de serveur NTP multiplateforme |
| [`network_syslog`](../../reference/vigo/resources/) | Configuration syslog multiplateforme |
| [`network_snmp`](../../reference/vigo/resources/) | Configuration de communauté SNMP multiplateforme |
| [`network_user`](../../reference/vigo/resources/) | Gestion d'utilisateurs locaux multiplateforme |
| [`network_banner`](../../reference/vigo/resources/) | Bannières de connexion/MOTD multiplateformes |
| [`network_interface`](../../reference/vigo/resources/) | État et description d'interface multiplateformes |
| [`network_vlan`](../../reference/vigo/resources/) | Gestion de VLAN multiplateforme |

| [`network_acl`](../../reference/vigo/resources/) | Entrées de liste de contrôle d'accès multiplateformes |
| [`network_static_route`](../../reference/vigo/resources/) | Gestion de routes statiques multiplateforme |
| [`network_dns`](../../reference/vigo/resources/) | Configuration de serveur DNS et de domaine multiplateforme |

## Traits des équipements

La passerelle collecte les traits de chaque équipement via SSH et les rend compte sous l'espace de noms `device` :

| Trait | Source | Description |
|-------|--------|-------------|
| `device.type` | Config | Type d'équipement (cisco_ios, etc.) |
| `device.model` | `show version` | Modèle matériel |
| `device.firmware` | `show version` | Version logicielle |
| `device.serial` | `show version` | Numéro de série |
| `device.hostname` | `show version` | Nom d'hôte de l'équipement |
| `device.uptime` | `show version` | Chaîne de durée de fonctionnement |
| `device.interfaces` | `show interfaces` | Liste des interfaces avec leur statut |
| `device.vlans` | `show vlan brief` | Table VLAN (Cisco uniquement) |
| `device.routes` | `show route summary` | Récapitulatif du nombre de routes |

Les traits sont disponibles dans les templates (`{{ .Traits.device.model }}`) et les expressions `when:` (`when: "trait('device.firmware', '>= 16.12')"`).

## Exemple de configcrate

```yaml
# stacks/configcrates/switch-base.vgo
name: switch-base
resources:
  - name: login-banner
    type: network_banner
    device_type: cisco_ios
    banner_type: login
    text: "Authorized access only."
    state: present

  - name: ntp-server
    type: network_ntp
    device_type: cisco_ios
    server: "10.0.0.1"
    state: present

  - name: syslog-server
    type: network_syslog
    device_type: cisco_ios
    syslog_host: "10.0.0.50"
    state: present
```

## Conformité et visibilité

Les équipements réseau participent pleinement au système de conformité de Vigo :

- **Suivi du statut** -- convergé, en rechute, divergé, dégradé, en échec, hors ligne ou aucune donnée
- **Historique d'exécutions** -- chaque exécution de convergence est enregistrée avec les résultats par ressource
- **Vues du parc** -- les équipements apparaissent dans `vigocli envoys list` et le tableau de bord de l'interface web
- **Requêtes de traits** -- `vigocli traits show <device-hostname>` affiche les faits de l'équipement

## Et ensuite

- **Écrire des configcrates contre les exécuteurs d'équipement** → voir `cisco_vlan`, `network_ntp`, `network_syslog`, `network_user` dans la référence des ressources. Les exécuteurs multiplateformes abstraient les différences entre types d'équipements là où c'est possible.
- **La clé d'hôte SSH d'un équipement a changé** → le cache TOFU génère une erreur bloquante ; enquêtez avant de l'effacer. Si le changement est légitime, supprimez la clé mise en cache sur la passerelle et reconvergez.
- **La passerelle ne peut pas joindre un équipement** → [Résoudre les problèmes courants](troubleshoot.md) couvre la matrice de connectivité du mandataire-passerelle.

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*
