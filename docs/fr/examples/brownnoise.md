<!-- vigo-i18n source_sha:daafcaecd580ca9d8b306ef49253688567c5a8ba48ae8d66090b2cf7025ecd76 -->

# Générateur de bruit brun

Cet exemple exécute du bruit brun en tant que **service systemd à l'échelle du système** qui joue
directement sur le périphérique **ALSA** local de l'hôte — sans Docker, sans PulseAudio, sans
session utilisateur connectée. `sox` génère le bruit, un fichier d'unité le supervise,
et une étape `amixer` idempotente règle le niveau de lecture. Il illustre une
chaîne `package` → `file` (script + unité) → `service` câblée avec
`notify`/`subscribes` afin qu'un changement de script ou d'unité redémarre le service, plus une
ressource `exec` avec une garde `unless:` tolérante qui converge une fois au lieu de
s'agiter à chaque cycle.

## Définition de configcrate

`stacks/configcrates/brownnoise.vgo` :

```yaml
name: brownnoise
description: Brown noise generator as a system-wide systemd service playing to the local ALSA device (no user session required)

vars:
  brownnoise_freq_center: "1786"
  brownnoise_wave: "0.08"
  brownnoise_minutes: "900"
  brownnoise_dir: /opt/brownnoise
  brownnoise_audio_dev: "plughw:0"
  brownnoise_alsa_card: "0"
  brownnoise_alsa_control: PCM
  brownnoise_alsa_level: "60"

resources:
  - name: brownnoise-sox
    type: package
    package: sox
    state: present

  - name: brownnoise-sox-formats
    type: package
    package: libsox-fmt-all
    state: present

  - name: brownnoise-alsa-utils
    type: package
    package: alsa-utils
    state: present

  # Set the ALSA mixer to a reasonable level and unmute. The unless: guard
  # accepts a tolerance band (level ± 3%) so it converges once rather than
  # re-running amixer every cycle if the card rounds the percentage.
  - name: brownnoise-volume
    type: exec
    command: amixer -c {{ .Vars.brownnoise_alsa_card }} sset {{ .Vars.brownnoise_alsa_control }} {{ .Vars.brownnoise_alsa_level }}% unmute && alsactl store
    unless: V=$(amixer -c {{ .Vars.brownnoise_alsa_card }} sget {{ .Vars.brownnoise_alsa_control }} | grep -oE '[0-9]+%' | head -n1 | tr -d '%'); amixer -c {{ .Vars.brownnoise_alsa_card }} sget {{ .Vars.brownnoise_alsa_control }} | grep -q '\[on\]' && [ "${V:-0}" -ge $(( {{ .Vars.brownnoise_alsa_level }} - 3 )) ] && [ "${V:-0}" -le $(( {{ .Vars.brownnoise_alsa_level }} + 3 )) ]
    depends_on: [brownnoise-alsa-utils]

  - name: brownnoise-dir
    type: directory
    state: present
    target_path: "{{ .Vars.brownnoise_dir }}"

  - name: brownnoise-script
    type: file
    state: present
    target_path: "{{ .Vars.brownnoise_dir }}/brownnoise.sh"
    owner: root
    group: root
    mode: "0755"
    source: templates/brownnoise.sh.tmpl
    depends_on: [brownnoise-dir]
    notify: [brownnoise-restart]

  - name: brownnoise-unit
    type: file
    state: present
    target_path: /etc/systemd/system/brownnoise.service
    owner: root
    group: root
    mode: "0644"
    content: |
      [Unit]
      Description=Brown noise generator
      After=sound.target

      [Service]
      # System-wide playback straight to ALSA — no PulseAudio/user session, so
      # it runs at boot regardless of who (if anyone) is logged in. Runs as root
      # for unconditional /dev/snd access; set brownnoise_audio_dev to pin a card
      # (e.g. hw:0, plughw:1) if the chosen device picks the wrong output.
      Type=simple
      Environment=AUDIODRIVER=alsa
      Environment=AUDIODEV={{ .Vars.brownnoise_audio_dev }}
      ExecStart={{ .Vars.brownnoise_dir }}/brownnoise.sh
      Restart=always
      RestartSec=5

      [Install]
      WantedBy=multi-user.target
    depends_on: [brownnoise-script]
    notify: [brownnoise-restart]

  - name: brownnoise-daemon-reload
    type: exec
    command: systemctl daemon-reload
    subscribes: [brownnoise-unit]

  - name: brownnoise-service
    type: service
    service: brownnoise
    state: running
    enabled: true
    depends_on: [brownnoise-unit, brownnoise-daemon-reload, brownnoise-script, brownnoise-sox, brownnoise-volume]

  - name: brownnoise-restart
    type: service
    when: changed
    service: brownnoise
    state: restarted
    depends_on: [brownnoise-service, brownnoise-daemon-reload]
```

## Affectation aux nœuds

`stacks/envoys/envoys.vgo` :

```yaml
envoys:
  - match: "workstation.home.example.com"
    roles: []
    configcrates:
      - brownnoise
    vars:
      brownnoise_audio_dev: "plughw:1"
      brownnoise_alsa_card: "1"
      brownnoise_freq_center: "1200"
```

## Modèle

Placez votre script `brownnoise.sh` à
`stacks/configcrates/templates/brownnoise.sh.tmpl`. Les vars ne sont utilisées que si
vous ajoutez des marqueurs de modèle — sinon, déposez le script tel quel :

```bash
minutes=${1:-'{{ .Vars.brownnoise_minutes }}'}
center=${2:-'{{ .Vars.brownnoise_freq_center }}'}
wave=${3:-'{{ .Vars.brownnoise_wave }}'}
```

Le script utilise le `play` de `sox` pour synthétiser du bruit brownien, le filtrer en passe-bande autour de
`center`, ajouter un trémolo lent (`wave`) plus de la réverbération, et répéter un segment d'une minute
pré-rendu afin de limiter l'utilisation du CPU. `AUDIODRIVER=alsa` + `AUDIODEV` (définis
dans l'unité) dirigent `play` vers le périphérique ALSA plutôt que vers un serveur PulseAudio
utilisateur.

## Fonctionnement

1. `brownnoise-sox`, `brownnoise-sox-formats` et `brownnoise-alsa-utils`
   installent `sox` (le synthétiseur/lecteur), ses codecs de format, et `amixer`/
   `alsactl`.
2. `brownnoise-volume` règle le contrôle du mélangeur ALSA à
   `brownnoise_alsa_level` (60 % par défaut) et le rétablit (unmute). La garde `unless:`
   relit le niveau actuel, accepte toute valeur à ±3 % de la cible tant qu'il n'est pas en sourdine,
   et seulement alors saute — il s'auto-répare donc d'un niveau en sourdine/erroné sans
   se ré-exécuter à chaque cycle de convergence. (Les gardes `exec` sont `unless:`,
   sauter-si-la-garde-réussit, et `onlyif:`, sauter-si-elle-échoue.)
3. `brownnoise-script` et `brownnoise-unit` écrivent le script du lecteur et
   l'unité systemd. Tous deux `notify: [brownnoise-restart]`, donc modifier l'un ou l'autre
   relance le service.
4. `brownnoise-daemon-reload` exécute `systemctl daemon-reload`, mais uniquement quand le
   fichier d'unité change (`subscribes: [brownnoise-unit]`).
5. `brownnoise-service` réconcilie l'unité vers `running` + `enabled` (démarre au
   boot). Il fait `depends_on:` des paquets, du script, de l'unité, du daemon-reload, et de
   l'étape de volume afin que le service ne démarre qu'une fois tout ce dont il a besoin en
   place.
6. `brownnoise-restart` est une ressource de service `when: changed` qui redémarre
   brownnoise lorsque son script ou son unité a changé lors de cette exécution.

**Changer le son :** modifiez `brownnoise.sh` (ou l'unité) — le câblage `notify`
redémarre le service à la prochaine convergence. Ajustez le centre de la passe-bande,
la profondeur du trémolo ou le volume en surchargeant les vars au niveau du nœud.

## Prérequis

Une carte son fonctionnelle avec un périphérique ALSA sur le nœud. Trouvez l'index de la carte et
le contrôle du mélangeur avec `aplay -l` et `amixer -c <n> scontrols` ; réglez
`brownnoise_alsa_card`, `brownnoise_audio_dev` et `brownnoise_alsa_control`
en conséquence (les contrôles courants sont `PCM`, `Master`, `Speaker`). Aucun serveur
d'affichage, session de connexion ou PulseAudio n'est requis — le service joue directement sur le
matériel en tant que root.

## Personnalisation

Surchargez les vars au niveau du nœud pour ajuster le son et cibler le bon périphérique :

| Variable | Défaut | Description |
|---|---|---|
| `brownnoise_freq_center` | `1786` | Fréquence centrale du filtre passe-bande en Hz |
| `brownnoise_wave` | `0.08` | Profondeur du trémolo (oscillation du volume) ; garder ≤ 0,20 |
| `brownnoise_minutes` | `900` | Durée en minutes (un segment d'une minute est répété) |
| `brownnoise_dir` | `/opt/brownnoise` | Répertoire de travail pour le script sur le nœud |
| `brownnoise_audio_dev` | `plughw:0` | Périphérique ALSA sur lequel `play` écrit (`AUDIODEV`) |
| `brownnoise_alsa_card` | `0` | Index de carte pour `amixer -c` |
| `brownnoise_alsa_control` | `PCM` | Contrôle du mélangeur à régler (p. ex. `PCM`, `Master`) |
| `brownnoise_alsa_level` | `60` | Pourcentage du niveau de lecture à régler et maintenir |
---
*Confidentiel -- Alexander4, LLC. Reproduction interdite. Voir [licence](../legal/license.md).*
