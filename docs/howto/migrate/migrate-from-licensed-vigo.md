# Migrate from a licensed Vigo release

Vigo 0.76.95 and later are open source with no node limit. Releases before that needed a signed `.lic` file and enforced a node count. This checklist takes a server from a licensed release to the current open-source one. Your database, secrets, TLS material, enrolled envoys and stacks carry over.

The current server refuses to start if `server.yaml` still contains a key it does not know, including the old `license:` block. Work through the server.yaml step before you restart.

## Before you start

- [ ] Take a backup: `sudo vigocli backup create /root/vigo-pre-migration.tar.gz --encrypt` (without `--encrypt` the archive leaves out the secrets master key), or copy `/srv/vigo/` while the server is stopped. See [Backup and recovery](../operate/backup-and-recovery.md).
- [ ] Keep a copy of your `.lic` file somewhere outside `/srv/vigo/` if you want a record of it. Vigo no longer reads it.
- [ ] Make sure the server host has Docker with the Compose plugin. Building from source instead of pulling the published image also needs the Buildx plugin, `git` and `rsync`.

## Get the new release

Pull the published image, or build it from source. Do one or the other.

- [ ] To pull it: `sudo docker pull ghcr.io/daniel-alexander4/vigo:latest`, then `sudo docker tag ghcr.io/daniel-alexander4/vigo:latest vigo:latest`. It carries the Linux agent for amd64 and arm64. Install its `vigocli` on the host: `id=$(sudo docker create vigo:latest) && sudo docker cp "$id":/usr/local/bin/vigocli /usr/local/bin/vigocli && sudo docker rm "$id"`.
- [ ] To build it: clone the source (`git clone https://github.com/daniel-alexander4/vigo.git && cd vigo`) and build without deploying: `sudo scripts/upgrade.sh full --norun`. Add `arm64` (`sudo scripts/upgrade.sh full arm64 --norun`) if you have arm64 envoys. This also installs the new `vigocli` on the host.

## Clean up server.yaml

- [ ] Delete the `license:` block (it held `mode: local` or `mode: aggregate`).
- [ ] Delete `license_dir:` from the `paths:` section.
- [ ] Delete a top-level `webhooks:` block if you have one. Outbound notifications are configured under `integrations:`; see [Server configuration](../../reference/server-yaml.md).
- [ ] Check the file with the new `vigocli`, run from `/srv/vigo`: `sudo vigocli doctor --pre-start --config /srv/vigo/server.yaml`. A line reading `field <name> not found` names a key you still need to remove.

## Deploy

- [ ] Deploy the new image. With a source checkout, run `sudo scripts/upgrade.sh deploy`. With the pulled image, run `cd /srv/vigo && sudo docker compose down`, then `sudo docker run --rm -v /srv/vigo:/srv/vigo vigo:latest --seed-only`, then `sudo docker compose up -d`. Don't use a plain `docker compose up -d` on its own, because the old container rewrites `docker-compose.yml` from its own image when it restarts.
- [ ] Watch the first start: `docker logs -f vigo`. Database migrations run before the server reports `server ready`, and on a large database they can take several minutes. The container shows as unhealthy until they finish; let it run.
- [ ] Confirm the server answers: `curl -sk https://127.0.0.1:8443/healthz` returns `200`.

## Remove what the license used

- [ ] Delete the license directory: `sudo rm -r /srv/vigo/license`.
- [ ] Remove `vigocli license ...` from any scripts or runbooks. The command no longer exists.
- [ ] In Prometheus, delete the `vigo-license` alert group and any rule or Grafana panel that uses a `vigo_license_*` metric. For fleet size, use `vigo_nodes_total` and `vigo_nodes_active_total`.
- [ ] If you run Connwaer, remove `license_dir` from its config. Connwaer 0.1.5 and later ignore it.

## Re-enroll envoys that need it

- [ ] Run `sudo vigocli doctor`. It lists any envoy with no enrolled client-certificate pin. The current server refuses those envoys at check-in.
- [ ] On each listed envoy, re-run the bootstrap: `curl -sSfk https://<server>:8443/bootstrap | sudo sh`. Re-enrolling records the pin.
- [ ] Envoys enrolled under a licensed release also hold a `COMMERCIAL-TERMS.md` and a stale `LICENSE` file in their data directory (`/etc/vigo-envoy/` on Linux, `C:\ProgramData\Vigo\` on Windows). A re-bootstrap replaces them with the Apache-2.0 `LICENSE` and `NOTICE`. On envoys you don't re-enroll, you can delete `COMMERCIAL-TERMS.md` by hand.

## Check the result

- [ ] `sudo vigocli doctor` reports no unpinned envoys.
- [ ] `sudo vigocli envoys list` shows every envoy checking in again.
- [ ] Envoys update their agent to the server's version on their own over the next few check-ins.

Spanner federation no longer needs a license. If you ran `license.mode: aggregate` across bolts, there is nothing to replace it with: every bolt enrolls without a cap.
