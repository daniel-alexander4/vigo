---
title: Set up local package mirrors
---

# Set up local package mirrors

You'll finish this page with one host in your fleet mirroring an upstream package archive (apt, dnf, apk, zypper, pacman, or Homebrew bottles) and the rest of the fleet pulling packages from that host over HTTP instead of from the public internet.

**When you'd use this:** the fleet pulls the same packages over and over; the bandwidth bill or the bootstrap-from-scratch time is real; some envoys are on slow / metered / firewalled links and can only reach internal hosts; you want to pin a known-good snapshot of a distro's archive while production-tested updates happen on a schedule.

**When you'd skip this:** the fleet is small enough that each envoy fetching from upstream isn't a problem, or you already have an enterprise package proxy (Artifactory, Pulp, Spacewalk) that handles this story.

## How it fits

Each `<pm>-mirror` configcrate is a self-contained stockpile recipe: install the upstream mirror tool, write its config, run it on cron, serve the resulting tree over HTTP via nginx. The same shape repeats across package managers — only the tooling, the upstream URL, and the on-disk layout change.

Not curator. Not the swarm. **One host runs the upstream mirror; the rest of the fleet points its package manager at that host's nginx.** That's the whole architecture. The reason to use a stockpile configcrate here rather than curator's P2P substrate is that when one host already has the bytes from upstream, there's no reason to also P2P-distribute them across the fleet — the bandwidth-save story is "fetch once from the internet, serve N times on the LAN."

Pair the mirror configcrate with the [nginx configcrate](../../reference/vigo/resources/file.md) on the same host. The mirror configcrate drops a `server` block into `/etc/nginx/conf.d/` and notifies the nginx service for reload — it does not install nginx itself.

## Shipped recipes

Each example lives at `example-configs/worksite/stockpile/configcrates/system/<pm>-mirror.vgo.example`. Copy into your stockpile, set vars to match your upstream of choice, publish.

| Configcrate | OS family | Tooling | Upstream | Client-side config |
|---|---|---|---|---|
| [`apt-mirror`](../../../example-configs/worksite/stockpile/configcrates/system/apt-mirror.vgo.example) | Debian / Ubuntu | `apt-mirror` (package) + cron + nginx | `archive.ubuntu.com/ubuntu` (or any Debian archive) | `deb http://<mirror>/<upstream-host>/<path> <distribution> <components>` in `/etc/apt/sources.list` |
| [`dnf-mirror`](../../../example-configs/worksite/stockpile/configcrates/system/dnf-mirror.vgo.example) | RHEL / Fedora / Rocky / AlmaLinux / Amazon Linux | `reposync` (dnf-utils) + `createrepo_c` + cron + nginx | configured `dnf` source on the mirror host | `baseurl=http://<mirror>/<repoid>/` in a `.repo` file |
| [`apk-mirror`](../../../example-configs/worksite/stockpile/configcrates/system/apk-mirror.vgo.example) | Alpine | `rsync` from `rsync://rsync.alpinelinux.org/alpine/` + nginx | Alpine official | `http://<mirror>/alpine/<version>/main` in `/etc/apk/repositories` |
| [`zypper-mirror`](../../../example-configs/worksite/stockpile/configcrates/system/zypper-mirror.vgo.example) | openSUSE / SLES | `dnf reposync` + `createrepo_c` (rpm-md, same shape as dnf) | openSUSE / SLES mirror | `baseurl=http://<mirror>/<repoid>/` in `/etc/zypp/repos.d/<repo>.repo` |
| [`pacman-mirror`](../../../example-configs/worksite/stockpile/configcrates/system/pacman-mirror.vgo.example) | Arch Linux | `rsync` from a tier-1 Arch mirror + nginx | configured tier-1 mirror | `Server = http://<mirror>/archlinux/$repo/os/$arch` in `/etc/pacman.d/mirrorlist` |
| [`brew-bottle-mirror`](../../../example-configs/worksite/stockpile/configcrates/system/brew-bottle-mirror.vgo.example) | macOS | nginx `proxy_cache` fronting `ghcr.io/v2/homebrew/core/` | GitHub Container Registry (Homebrew's bottle store) | `export HOMEBREW_BOTTLE_DOMAIN=http://<mirror>` on each client |

## Different shape: `brew-bottle-mirror`

Five of the six examples follow the same pattern — rsync (or rsync-like) from upstream, then serve the resulting directory tree from disk. `brew-bottle-mirror` is the odd one out: Homebrew bottles live on GitHub Container Registry as OCI blobs, not on a traditional rsync-able mirror tree. The pragmatic shape on macOS is **a transparent caching reverse proxy** — nginx with `proxy_cache_path` fronting `ghcr.io`. First fetch goes to GitHub, every subsequent fetch is served from local disk. Clients set `HOMEBREW_BOTTLE_DOMAIN` to the mirror host and `brew install` does the rest.

The trade-offs: no offline-from-day-one (the first miss must reach upstream), but no preflight rsync either (the cache fills in response to demand). For a typical macOS-laptop fleet this is the operationally-correct shape.

## Deferred recipes — pattern is documented, the example isn't shipped

These ecosystems are workable in principle but aren't included in the current shipping set. Each entry lists *what tooling you'd use* if you wrote the configcrate yourself; the pattern is identical to the shipped examples (install tool, cron, nginx).

### Windows

- **Chocolatey** — NuGet feeds. Internal-repo story uses a NuGet server (Chocolatey Server / ProGet / static `.nupkg` directory). The `--internalize` flag that lets `choco download` rewrite community packages for internal serving is a Chocolatey Pro feature. Free path: serve a static `.nupkg` directory via IIS or nginx + `choco push` packages you've manually downloaded. Deferred per Vigo's Linux-first cadence — no Windows new-feature work until a customer with a real Windows fleet asks.
- **Scoop** — git-based buckets. "Mirroring" means a local git mirror of the bucket repo plus an HTTP server in front of it. Smaller-userbase tool; deferred for the same reason.
- **Winget** — REST API + manifest tree. Source-mirror means hosting your own private source. Workable but more involved than the file-tree mirrors above; deferred.

### BSD / illumos

- **FreeBSD `pkg`** — `pkg fetch -d` into a directory + nginx serving it; clients point `pkg`'s `url` at the mirror.
- **OpenBSD `pkg_add`** — `rsync` from an OpenBSD mirror; `pkg_add` reads `PKG_PATH` URLs.
- **illumos / Solaris IPS** — `pkgrecv` into a local repository, `pkg.depotd` (or nginx in front of `/var/share/pkg/repositories/`) to serve. Workable; deferred until lab demand.
- **MacPorts** — rsync from upstream MacPorts; less commonly mirrored.

### Language registries

These are a different category — "language package index mirrors" rather than "OS package mirrors" — and the tooling typically runs its own daemon rather than producing a static tree nginx can serve. If you want them, file a separate item:

- **PyPI** → `devpi-server` or `bandersnatch`
- **npm** → `verdaccio` (full registry) or `npm proxy`
- **RubyGems** → `geminabox` or `rubygems-mirror`
- **Go modules** → Athens (`GOPROXY`)
- **Cargo** → custom alternative-registry mirror

### Container images

Already covered by curator's Docker Registry v2 shim — see [Set up curator](../swarm/set-up-curator.md). Not a package-mirror configcrate concern.

## Operational notes

- **One mirror host per family** is the v1 shape. A single Vigo-managed mirror host can run several of these configcrates at once (one per family) — `apt-mirror` for the Ubuntu envoys, `dnf-mirror` for the Rocky envoys, `apk-mirror` for the Alpine envoys, etc. They serve under non-conflicting `location` blocks of the same nginx instance and disk paths.
- **Disk usage** is significant. A full mirror of Ubuntu main + restricted + universe + multiverse for one release runs around 250–400 GiB. Plan disk before publishing. The `apt-mirror`'s `clean` directive will trim deleted upstream packages, but the working set is the working set.
- **Cron schedule** defaults to nightly in each example (`0 4 * * *`). On a slow uplink, stretch this to weekly — fleet convergence isn't blocked on the mirror being fresh, only on it being up.
- **Sources of truth.** If the mirror falls behind (cron failed, disk full, upstream changed), client envoys can still upgrade — apt/dnf/apk will retry from the upstream URLs in the source list. Add the mirror as the FIRST source and keep the upstream as a fallback. The examples set this up.
- **First-run convergence.** The initial rsync/reposync takes hours and Vigo's per-resource convergence timeout (default 300s for `exec` / `package` / `repository` resources, per `agent/src/runner/mod.rs`) will kill an inline sync. The examples deliberately set up cron and let the first sync happen out-of-band — operator runs it once by hand or waits for the first cron tick.
- **Retraction.** Each configcrate has a paired retract file at `example-configs/retractions/configcrates/system/<pm>-mirror-retract.vgo.example`. Retract removes the cron entry, the nginx site, and (where applicable) the mirror tool package, but **leaves the mirrored tree on disk** — operators delete that manually after confirming the fleet has flipped back to upstream. The disk delete isn't reversible and shouldn't be a convergence-cycle side-effect.

## When something goes wrong

| Symptom | Likely cause | Where to look |
|---|---|---|
| Mirror tree empty or stale | First cron tick hasn't run yet, or cron failed | `journalctl -u cron`, the mirror tool's log under `/var/log/<pm>-mirror/` |
| Clients get 404s on package metadata | nginx `location` block wrong, or upstream archive layout changed | `/var/log/nginx/<pm>-mirror.access.log` for the actual paths requested |
| Disk full | The working set is bigger than expected; `clean` on apt-mirror | `df -h /var/spool/<pm>-mirror`; trim the components list if you don't need all of them |
| Clients fall back to upstream on every install | Mirror host unreachable, certificate name mismatch, or sources.list points at the upstream first | `apt-cache policy <pkg>` (or equivalent) to confirm which source is being preferred |
