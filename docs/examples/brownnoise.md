
# Brown Noise Generator

This example builds a brown-noise Docker image on a managed node and runs it
as a first-class `container:` resource, with audio piped to the host's
PulseAudio server. It demonstrates an idempotent local image build (`exec`
guarded by `unless:`) feeding the `container:` resource, with an
immutable image tag (`brownnoise_version`) as the single rebuild-and-restart
lever.

## Configcrate Definition

`stacks/configcrates/brownnoise.vgo`:

```yaml
name: brownnoise
description: Brown noise generator in a Docker container
vars:
  brownnoise_version: "1"
  brownnoise_freq_center: "1786"
  brownnoise_wave: "0.08"
  brownnoise_minutes: "900"
  brownnoise_dir: "/opt/brownnoise"
  brownnoise_pulse_uid: "1000"
  brownnoise_pulse_user: "youruser"

resources:
  - name: brownnoise-dir
    type: directory
    target_path: "{{ .Vars.brownnoise_dir }}"
    state: present

  - name: brownnoise-script
    type: file
    target_path: "{{ .Vars.brownnoise_dir }}/brownnoise.sh"
    state: present
    owner: root
    group: root
    mode: "0755"
    source: templates/brownnoise.sh.tmpl
    depends_on:
      - brownnoise-dir

  - name: brownnoise-dockerfile
    type: file
    target_path: "{{ .Vars.brownnoise_dir }}/Dockerfile"
    state: present
    owner: root
    group: root
    mode: "0644"
    content: |
      FROM ubuntu:24.04
      RUN apt-get update && apt-get install -y --no-install-recommends sox libsox-fmt-all pulseaudio-utils && rm -rf /var/lib/apt/lists/*
      COPY brownnoise.sh /usr/local/bin/brownnoise.sh
      CMD ["/usr/local/bin/brownnoise.sh"]
    depends_on:
      - brownnoise-dir

  - name: brownnoise-image
    type: exec
    command: "docker build -t brownnoise:{{ .Vars.brownnoise_version }} {{ .Vars.brownnoise_dir }}"
    unless: "docker image inspect brownnoise:{{ .Vars.brownnoise_version }}"
    depends_on:
      - brownnoise-script
      - brownnoise-dockerfile

  - name: brownnoise-container
    type: container
    container: brownnoise
    image: "brownnoise:{{ .Vars.brownnoise_version }}"
    state: running
    pull: never
    restart: unless-stopped
    env: "PULSE_SERVER=unix:/run/user/{{ .Vars.brownnoise_pulse_uid }}/pulse/native"
    volumes: "/run/user/{{ .Vars.brownnoise_pulse_uid }}/pulse/native:/run/user/{{ .Vars.brownnoise_pulse_uid }}/pulse/native,/home/{{ .Vars.brownnoise_pulse_user }}/.config/pulse/cookie:/root/.config/pulse/cookie:ro"
    depends_on:
      - brownnoise-image
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
      brownnoise_pulse_uid: "1000"
      brownnoise_pulse_user: "alice"
      brownnoise_freq_center: "1200"
```

## Template

Place your `brownnoise.sh` script at `stacks/configcrates/templates/brownnoise.sh.tmpl`. You can optionally template the parameters:

```bash
minutes=${1:-'{{ .Vars.brownnoise_minutes }}'}
center=${2:-'{{ .Vars.brownnoise_freq_center }}'}
wave=${3:-'{{ .Vars.brownnoise_wave }}'}
```

Or just drop the script in as-is — the vars are only used if you add template markers.

## How It Works

1. `brownnoise-dir` creates `/opt/brownnoise` on the node.
2. The script and Dockerfile are written into that directory.
3. `brownnoise-image` runs `docker build` only if the tagged image doesn't already exist — the `unless:` guard runs `docker image inspect brownnoise:<version>` and skips the build when it succeeds. (`exec` idempotency guards are `unless:`, skip-if-the-guard-succeeds, and `onlyif:`, skip-if-it-fails.)
4. `brownnoise-container` is a first-class `container:` resource. It reconciles to `state: running`: creates and starts the container if absent, starts it if stopped, and stops-removes-recreates it if the running container's image differs from `brownnoise:<version>`. Because the lifecycle is owned by the executor, there's no `docker run --name` collision and the resource self-heals from a crashed or stopped container. `pull: never` keeps it from trying to fetch the locally-built image from a registry.
5. The PulseAudio socket and cookie are bind-mounted into the container so `sox`/`play` sends audio to the host's sound system.

**Changing the sound:** edit `brownnoise.sh` (or the Dockerfile) and **bump `brownnoise_version`**. The new tag makes `unless:` miss, so the image rebuilds, and the container's running image (`brownnoise:<old>`) no longer matches the desired tag, so the `container:` resource recreates it. The version tag is the single lever for "rebuild and restart" — editing the script *without* bumping the version is a no-op, which is the intended immutable-image behavior.

## Requirements

Docker must be installed and the daemon running on the node. If the configcrate also manages Docker (a `package` + `service` resource), have `brownnoise-image` and `brownnoise-container` `depends_on:` the Docker service resource so the daemon is up before any build or run.

## Customization

Override vars at the node level to tune the sound:

| Variable | Default | Description |
|---|---|---|
| `brownnoise_version` | `1` | Image tag. Bump it whenever you change the script or Dockerfile to trigger a rebuild + container recreate. |
| `brownnoise_freq_center` | `1786` | Band-pass filter center frequency in Hz |
| `brownnoise_wave` | `0.08` | Tremolo depth (volume oscillation) |
| `brownnoise_minutes` | `900` | Duration in minutes |
| `brownnoise_pulse_uid` | `1000` | UID of the user whose PulseAudio socket to use |
| `brownnoise_pulse_user` | `youruser` | Username whose `~/.config/pulse/cookie` to mount (container `volumes:` are not shell-evaluated, so this is a plain var, not `$(id -un)`) |
| `brownnoise_dir` | `/opt/brownnoise` | Working directory on the node |
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../legal/license.md).*
