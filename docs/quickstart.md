# Quickstart

Stand up the Vigo server, enroll your first envoy, and apply your first configcrate. About ten minutes of copy-paste with the published image. If you'd rather be walked through it step by step, take the [tutorials](start/) instead — same path, more explanation.

## 1. Start the server

You can pull the published server image or build it yourself. Either way the image ends up tagged `vigo:latest`, the name the generated compose file runs.

**Pull the published image.** This is the quick path. The host needs Docker with the Compose plugin and an x86_64 CPU; on Ubuntu, `sudo apt-get install -y docker.io docker-compose-v2` installs them. The image is signed with the project's release key, so envoys update their agent on their own when you move to a newer image. It carries the Linux agent for amd64 and arm64. To pin a release, use its version tag, such as `:0.80.0`, in place of `:latest`.

```bash
sudo docker pull ghcr.io/daniel-alexander4/vigo:latest
sudo docker tag ghcr.io/daniel-alexander4/vigo:latest vigo:latest
IMAGE=vigo:latest
```

**Or build it from source.** The build runs inside Docker. The host needs Docker with the Compose and Buildx plugins, plus `git` and `rsync`; on Ubuntu, `sudo apt-get install -y git rsync docker.io docker-compose-v2 docker-buildx` installs them. The first build takes about 20 minutes. The optional signing key lets envoys update their agent on their own; without it, every upgrade means re-running the bootstrap on each envoy. Creating it needs Go 1.25, and you should keep the key safe.

```bash
git clone https://github.com/daniel-alexander4/vigo.git && cd vigo
go run ./server/hardening/cmd/sign -genkey -output signing.key   # optional: lets envoys self-update
sudo scripts/upgrade.sh full --norun   # builds the server image and the amd64 agent
IMAGE=vigo:latest   # the tag the build gives the local image
```

```bash
sudo mkdir -p /srv/vigo
sudo docker run --rm -v /srv/vigo:/srv/vigo "$IMAGE" --seed-only
sudo sed -i "s|^  # hostname: .*|  hostname: $(hostname)|" /srv/vigo/server.yaml
cd /srv/vigo
sudo docker compose up -d
```

`--seed-only` writes `/srv/vigo/docker-compose.yml`, `server.yaml`, `.env`, and example configs, then exits. The `sed` line sets `server.hostname` in `server.yaml` to this host's name, which the server needs to find its own agent when you create your admin login; you can also edit the file by hand before starting. `docker compose up -d` starts the server, which generates its CA and TLS certificates under `/srv/vigo/tls/` on first start. REST + Web UI on `https://localhost:8443`, gRPC on `localhost:1530`.

Single-`docker run` if you don't want compose:

```bash
sudo mkdir -p /srv/vigo
sudo docker run -d --name vigo \
  --network host \
  -v /srv/vigo:/srv/vigo \
  -v /usr/local/bin:/host-bin \
  "$IMAGE"
```

`--network host` binds 8443 (REST/UI), 1530 (gRPC), and 1531 (swarm) directly on the host — matching the generated compose, which host-networks the server so agent traffic doesn't route through Docker's per-port userland proxy. The `/host-bin` mount is how the container installs `vigocli` on the host. (Scrier's `guacd` sidecar isn't started by this bare `docker run`; use compose if you need RDP/VNC scrier sessions.)

Open `https://localhost:8443` and accept the self-signed cert. There is no default admin login; you create yours in step 3, after the server's own agent is enrolled.

## 2. Enroll envoys

**The server's own host first.** Every Vigo server runs a co-hosted `vigo` agent so it can participate in swarm operations alongside its envoys. From the server host — **use the IPv4 loopback literal `127.0.0.1`, not `localhost` and not the host's own hostname**; the agent dials whatever address the curl URL used on every subsequent check-in, and only loopback is resolver-independent:

```bash
curl -sSfk https://127.0.0.1:8443/bootstrap | sudo sh
```

Then any *other* machine you want to manage — note this is a separate command shape, by design, because remote envoys reach the server by hostname / LAN IP while the server's own envoy reaches itself by loopback:

```bash
# On the envoy:
curl -sSfk https://<server-ip>:8443/bootstrap | sudo sh
```

The `-k` is intentional. The Vigo server runs its own CA and presents a self-signed cert. Trust gets established on the *next* step: the bootstrap script downloads the agent, generates a per-host ED25519 keypair, and registers it with the server (token-free from loopback and private-LAN CIDRs by default; require a one-time token for anything outside the trusted-enrollment ranges — mint one with `vigocli tokens generate`, bind it with `vigocli approved add '*.example.com' --token <token>`, and fetch `/bootstrap?token=<token>`). Every subsequent gRPC call between the agent and the server is mTLS-pinned against that pair — `-k` only relaxes the one-time bootstrap fetch, not the steady-state security.

The bootstrap script also installs the per-OS service (systemd / launchd / rc.d / rcctl / SMF / Windows Service).

After enrollment each envoy appears in the dashboard at `https://localhost:8443/envoys` and checks in at the interval set by `checkin.interval` in `server.yaml`.

## 3. Create your login

Admin logins map to real accounts on the server host: the account needs a login shell and an SSH public key in `~/.ssh/id_ed25519.pub` or `id_rsa.pub` (create one with `ssh-keygen -t ed25519` as that user if it has none). The server checks the account against its own agent's report, using the `server.hostname` you set in step 1. Initialize the secrets vault, then create the admin; `webusers create` asks for the login password.

```bash
sudo vigocli secrets init          # sets the vault passphrase
sudo vigocli secrets unlock
sudo vigocli webusers create --username <you> --role admin
```

The server checks the account against the list of accounts the agent last reported. If `webusers create` says the agent hasn't reported yet, or that the account isn't a human user because you created it after step 2, wait for the agent's next check-in (up to about five minutes) and run it again.

Sign in at `https://localhost:8443/login` as `<you>`.

## 4. Write a configcrate

```yaml
# /srv/vigo/stacks/configcrates/motd.vgo
name: motd
resources:
  - name: motd-file
    type: file
    target_path: /etc/motd
    state: present
    content: |
      ========================================
      Managed by Vigo
      Hostname: {{ .Traits.network.hostname }}
      OS: {{ .Traits.os.distro }}
      ========================================
    owner: root
    group: root
    mode: "0644"
```

## 5. Assign the configcrate to envoys

An envoy gets configuration only when an entry in a hostcrate matches its hostname. The simplest hostcrate gives the configcrate to every envoy:

```yaml
# /srv/vigo/stacks/envoys.vgo
envoys:
  - match: "*"
    configcrates: [motd]
```

For per-host assignment, put more specific entries above the catch-all; the first match wins. In this example, `nginx` and `postgres` stand for configcrates you have written yourself:

```yaml
# /srv/vigo/stacks/envoys.vgo
envoys:
  - match: "web*.prod"
    configcrates: [motd, nginx]
  - match: "db*.prod"
    configcrates: [motd, postgres]
  - match: "*"
    configcrates: [motd]
```

Listing configcrates in the seeded `common.vgo` does not, on its own, give them to any envoy.

## 6. Publish

```bash
sudo vigocli config publish
```

Validates the tree, syncs to `/srv/vigo/.live/`, triggers a server reload. If validation fails, it prints what is wrong and in which file. Add `--ask-ai` to also get fix suggestions from the AI assistant, if you have configured one.

## 7. Wait for the next check-in

Envoys pick up the published config at their next check-in. With the seeded `checkin.interval` of five minutes (plus up to 20% jitter), that is at most about six minutes.

## 8. Verify

```bash
sudo vigocli runs list --limit 5
```

Or the Web UI at `https://localhost:8443/runs`. You should see `motd` applied successfully on every enrolled envoy.

## Where to go next

- [Write your first real configcrate](howto/configcrates/write-your-first-configcrate.md) — packages, services, dependencies, conditionals
- [Set up Scrier](howto/operate/set-up-scrier.md) — admin remote-access (SSH / RDP / VNC) without inbound ports
- [Compliance reporting](howto/compliance/compliance-reporting.md) — turn configcrate tags into HIPAA / SOC 2 / PCI / NIST coverage reports
- [The configcrate language](reference/configcrate-language.md) — the full configcrate-authoring guide
- [Reference / vigocli](reference/vigocli/) — every CLI command
- [Reference / agent resources](reference/vigo/resources/) — every executor type
- [Reference / agent traits](reference/vigo/traits/) — every trait collector
