
---
title: illumos Agent Support
weight: 60
---

# illumos Agent Support

Vigo supports illumos-based systems including OmniOS, OpenIndiana, and SmartOS. The agent provides native integration with SMF (Service Management Facility) and IPS (Image Packaging System).

## Supported Distributions

- **OmniOS** (OmniTI/OmniOS Community Edition)
- **OpenIndiana** (Hipster)
- **SmartOS** (Joyent)

Architecture: x86_64 (amd64) only.

## Bootstrap

```sh
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

The bootstrap script detects illumos via `uname -s` returning `SunOS`, downloads the agent binary, and installs an SMF service.

## SMF Service

The agent runs as an SMF service with FMRI `svc:/system/vigo-envoy:default`.

```sh
# Check status
svcs vigo-envoy

# Restart
svcadm restart vigo-envoy

# View logs
svcs -x vigo-envoy
```

The SMF manifest is installed at `/var/svc/manifest/system/vigo-envoy.xml`.

## Executors

The following executors are available on illumos:

| Executor | Implementation |
|----------|---------------|
| `service` | SMF via `svcadm`/`svcs` |
| `package` | IPS via `pkg install`/`pkg uninstall`/`pkg list` |
| `hostname` | Writes `/etc/nodename` |
| `timezone` | Updates `TZ=` in `/etc/default/init` |
| `user` | Standard Unix `useradd`/`usermod`/`userdel` |
| `group` | Standard Unix `groupadd`/`groupmod`/`groupdel` |
| `cron` | Standard Unix crontab |
| `file`, `exec`, `directory`, `symlink` | Cross-platform |

## Trait Collectors


illumos has native trait collectors for:

| Collector | Data Source |
|-----------|------------|
| `os` | `/etc/release`, `uname` |
| `hardware` | `psrinfo -p`, `prtconf`, `kstat` |
| `memory` | `prtconf`, `kstat unix:0:system_pages:freemem`, `pagesize` |
| `network` | `ipadm show-addr`, `dladm show-phys` |
| `uptime` | `kstat unix:0:system_misc:boot_time` |
| `kernel` | `uname -v/-r` |
| `swap` | `swap -l` |
| `mountpoints` | `mount` output |
| `filesystem` | `df -k` |
| `dmi` | `smbios -t SMB_TYPE_SYSTEM` |
| `virtual` | `zonename` (zone detection) |
| `packages` | `pkg list -H` (IPS) |

Shared collectors (identity, loadavg, locale, processors, users, gpu) use portable Unix commands.

## Building

illumos builds require a native illumos host or cross-compilation toolchain:

```sh
# On an illumos host:
rustup target add x86_64-unknown-illumos
make dist-agent-illumos
```

The binary is output to `dist/agent/illumos/amd64/vigo`.

## Limitations

- **Zones/ZFS**: Not managed. Use illumos-native tooling for zone and ZFS administration.
- **Oracle Solaris**: Not supported. illumos-only.
- **Architecture**: x86_64 only (no SPARC support).
- **Cross-compilation**: Native builds are recommended. Cross-compilation from Linux requires an illumos sysroot.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
