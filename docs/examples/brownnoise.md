
# Brown Noise Generator

This example builds and runs a brown noise Docker container on a managed node, with audio output piped to the host's PulseAudio server. It demonstrates using `exec` resources with `check` for idempotent Docker image builds and container lifecycle, plus `subscribes` to automatically rebuild and restart when files change.

## Module Definition

`stockpile/configcrates/brownnoise.vgo`:

```yaml
name: brownnoise
description: Brown noise generator in a Docker container
vars:
  brownnoise_freq_center: "1786"
  brownnoise_wave: "0.08"
  brownnoise_minutes: "900"
  brownnoise_dir: "/opt/brownnoise"
  brownnoise_pulse_uid: "1000"

resources:
  - name: brownnoise-dir
    type: directory
    target_path: "{{ .Vars.brownnoise_dir }}"

  - name: brownnoise-script
    type: file
    target_path: "{{ .Vars.brownnoise_dir }}/brownnoise.sh"
    owner: root
    group: root
    mode: "0755"
    source: templates/brownnoise.sh.tmpl
    depends_on:
      - brownnoise-dir

  - name: brownnoise-dockerfile
    type: file
    target_path: "{{ .Vars.brownnoise_dir }}/Dockerfile"
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
    command: "docker build -t brownnoise {{ .Vars.brownnoise_dir }}"
    check: "docker image inspect brownnoise"
    depends_on:
      - brownnoise-script
      - brownnoise-dockerfile
    subscribes:
      - brownnoise-script
      - brownnoise-dockerfile

  - name: brownnoise-container
    type: exec
    command: >-
      docker run -d --name brownnoise --restart unless-stopped
      -e PULSE_SERVER=unix:/run/user/{{ .Vars.brownnoise_pulse_uid }}/pulse/native
      -v /run/user/{{ .Vars.brownnoise_pulse_uid }}/pulse/native:/run/user/{{ .Vars.brownnoise_pulse_uid }}/pulse/native
      -v /home/$(id -un {{ .Vars.brownnoise_pulse_uid }})/.config/pulse/cookie:/root/.config/pulse/cookie:ro
      brownnoise
    check: "docker ps -q --filter name=brownnoise --filter status=running"
    depends_on:
      - brownnoise-image
    subscribes:
      - brownnoise-image
```

## Node Assignment

`stockpile/envoys/envoys.vgo`:

```yaml
envoys:
  - match: "workstation.home.example.com"
    roles: []
    configcrates:
      - brownnoise
    vars:
      brownnoise_pulse_uid: "1000"
      brownnoise_freq_center: "1200"
```

## Template

Place your `brownnoise.sh` script at `stockpile/configcrates/templates/brownnoise.sh.tmpl`. You can optionally template the parameters:

```bash
minutes=${1:-'{{ .Vars.brownnoise_minutes }}'}
center=${2:-'{{ .Vars.brownnoise_freq_center }}'}
wave=${3:-'{{ .Vars.brownnoise_wave }}'}
```

Or just drop the script in as-is — the vars are only used if you add template markers.

## How It Works

1. The `brownnoise-dir` resource creates `/opt/brownnoise` on the node.
2. The script and Dockerfile are written into that directory.
3. The `brownnoise-image` exec runs `docker build` only if the image doesn't already exist (`check` runs `docker image inspect`). It `subscribes` to both files, so if either changes on a subsequent convergence, the image is rebuilt.
4. The `brownnoise-container` exec runs `docker run` only if no running container named `brownnoise` exists. It `subscribes` to the image resource, so a rebuild triggers a container restart.
5. The PulseAudio socket is mounted into the container so `sox`/`play` sends audio to the host's sound system.

## Customization

Override vars at the node level to tune the sound:

| Variable | Default | Description |
|---|---|---|
| `brownnoise_freq_center` | `1786` | Band-pass filter center frequency in Hz |
| `brownnoise_wave` | `0.08` | Tremolo depth (volume oscillation) |
| `brownnoise_minutes` | `900` | Duration in minutes |
| `brownnoise_pulse_uid` | `1000` | UID of the user whose PulseAudio socket to use |
| `brownnoise_dir` | `/opt/brownnoise` | Working directory on the node |
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../legal/license.md).*
