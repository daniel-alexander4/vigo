
# Brown Noise Generator

This example runs brown noise as a **system-wide systemd service** that plays
straight to the host's local **ALSA** device — no Docker, no PulseAudio, no
logged-in user session. `sox` generates the noise, a unit file supervises it,
and an idempotent `amixer` step sets the playback level. It demonstrates a
`package` → `file` (script + unit) → `service` chain wired with
`notify`/`subscribes` so a script or unit change restarts the service, plus an
`exec` resource with a tolerant `unless:` guard that converges once instead of
churning every cycle.

## Configcrate Definition

`stacks/configcrates/brownnoise.vgo`:

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

## Node Assignment

`stacks/envoys/envoys.vgo`:

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

## Template

Place your `brownnoise.sh` script at
`stacks/configcrates/templates/brownnoise.sh.tmpl`. The vars are only used if
you add template markers — drop the script in as-is otherwise:

```bash
minutes=${1:-'{{ .Vars.brownnoise_minutes }}'}
center=${2:-'{{ .Vars.brownnoise_freq_center }}'}
wave=${3:-'{{ .Vars.brownnoise_wave }}'}
```

The script uses `sox`'s `play` to synthesize Brownian noise, band-pass it around
`center`, add a slow tremolo (`wave`) plus reverb, and repeat a pre-rendered
one-minute segment to keep CPU usage low. `AUDIODRIVER=alsa` + `AUDIODEV` (set
in the unit) point `play` at the ALSA device rather than a user PulseAudio
server.

## How It Works

1. `brownnoise-sox`, `brownnoise-sox-formats`, and `brownnoise-alsa-utils`
   install `sox` (the synthesizer/player), its format codecs, and `amixer`/
   `alsactl`.
2. `brownnoise-volume` sets the ALSA mixer control to
   `brownnoise_alsa_level` (60% by default) and unmutes it. The `unless:` guard
   reads the current level back, accepts anything within ±3% of the target while
   unmuted, and only then skips — so it self-heals a muted/wrong level without
   re-running every convergence cycle. (`exec` guards are `unless:`,
   skip-if-the-guard-succeeds, and `onlyif:`, skip-if-it-fails.)
3. `brownnoise-script` and `brownnoise-unit` write the player script and the
   systemd unit. Both `notify: [brownnoise-restart]`, so editing either
   bounces the service.
4. `brownnoise-daemon-reload` runs `systemctl daemon-reload`, but only when the
   unit file changes (`subscribes: [brownnoise-unit]`).
5. `brownnoise-service` reconciles the unit to `running` + `enabled` (starts at
   boot). It `depends_on:` the packages, script, unit, daemon-reload, and the
   volume step so the service comes up only once everything it needs is in
   place.
6. `brownnoise-restart` is a `when: changed` service resource that restarts
   brownnoise when its script or unit changed this run.

**Changing the sound:** edit `brownnoise.sh` (or the unit) — the `notify` wiring
restarts the service on the next convergence. Tune the band-pass center,
tremolo depth, or volume by overriding the vars at the node level.

## Requirements

A working sound card with an ALSA device on the node. Find the card index and
mixer control with `aplay -l` and `amixer -c <n> scontrols`; set
`brownnoise_alsa_card`, `brownnoise_audio_dev`, and `brownnoise_alsa_control`
accordingly (common controls are `PCM`, `Master`, `Speaker`). No display
server, login session, or PulseAudio is required — the service plays straight to
hardware as root.

## Customization

Override vars at the node level to tune the sound and target the right device:

| Variable | Default | Description |
|---|---|---|
| `brownnoise_freq_center` | `1786` | Band-pass filter center frequency in Hz |
| `brownnoise_wave` | `0.08` | Tremolo depth (volume oscillation); keep ≤ 0.20 |
| `brownnoise_minutes` | `900` | Duration in minutes (a one-minute segment is repeated) |
| `brownnoise_dir` | `/opt/brownnoise` | Working directory for the script on the node |
| `brownnoise_audio_dev` | `plughw:0` | ALSA device `play` writes to (`AUDIODEV`) |
| `brownnoise_alsa_card` | `0` | Card index for `amixer -c` |
| `brownnoise_alsa_control` | `PCM` | Mixer control to set (e.g. `PCM`, `Master`) |
| `brownnoise_alsa_level` | `60` | Playback level percentage to set and hold |
